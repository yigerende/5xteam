package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
	"chatgpt-space-merge/internal/workflow"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type siwcBrokerContextKey struct{}
type siwcBroker struct {
	s                              *Server
	ctx                            context.Context
	email, password, hostID, proxy string
	settings                       model.ProSettings
	generated                      *sub2.SIWCAuthURL
	paid                           bool
}

func (b *siwcBroker) call(method string, args map[string]any) (any, error) {
	if method == "siwc_generate" {
		if b.generated != nil {
			return *b.generated, nil
		}
		profile, _, e := b.s.store.MailAccountCredential(b.email)
		if e != nil {
			return nil, e
		}
		current, password, _, e := b.s.proNewPushSettings(profile)
		if e != nil {
			return nil, e
		}
		if current.Provider != "sub2" || current.Sub2.AuthMode != "siwc" || current.Sub2.SIWCAuthorizationSource != "sub2api" {
			return nil, errors.New("推送授权方式已改变，请按当前配置重试授权")
		}
		if len(current.Sub2.GroupIDs) == 0 {
			return nil, errors.New("请选择 SIWC 推送分组")
		}
		b.settings, b.password = current, password
		result, err := b.s.sub2.GenerateSIWCAuthURL(b.ctx, b.settings.Sub2, b.password, b.email, b.hostID, b.proxy)
		if err == nil {
			b.generated = &result
		}
		return result, err
	}
	if method != "siwc_callback" || b.generated == nil {
		return nil, errors.New("SIWC s2接口会话不存在")
	}
	callback, _ := args["callback_url"].(string)
	web, _ := args["web_session"].(map[string]any)
	at, _ := web["access_token"].(string)
	if at == "" {
		at, _ = web["accessToken"].(string)
	}
	info, err := workflow.DecodeUserInfo(at)
	if err != nil || !strings.EqualFold(info.Email, b.email) {
		return nil, errors.New("SIWC 原登录会话账号校验失败")
	}
	p, _, err := b.s.store.MailAccountCredential(b.email)
	if err != nil {
		return nil, err
	}
	if b.paid {
		_, snapshot, e := b.s.store.GPTPayOrder(p.ProAuto.OrderID)
		if e != nil || p.ProAuto.Steps["recharge"] != "completed" || info.AccountID != snapshot.Input.Session.Account.ID {
			return nil, errors.New("SIWC 网页凭证与本次开通空间不匹配，停止导入")
		}
	}
	p, groups, err := b.s.pinProSub2Push(p, b.settings, b.password)
	if err != nil {
		return nil, err
	}
	input := sub2.CreateAccountInput{Name: proAccountName(p), GroupIDs: groups, Models: b.settings.Sub2.Models, Concurrency: b.settings.Sub2.AccountConcurrency, Priority: b.settings.Sub2.Priority, CpaWS: b.settings.Sub2.CpaWS,
		SIWCImport: &sub2.SIWCImportInput{SessionID: b.generated.SessionID, CallbackURL: callback, Email: b.email, HostID: b.hostID, WebAccessToken: at, WebAccountID: info.AccountID}}
	created, err := b.s.createSub2WithProxy(b.ctx, b.settings.Sub2, b.password, input, "pro-siwc:"+p.ID, "pro-siwc-"+p.ID, nil)
	if err != nil {
		return nil, err
	}
	if _, err = b.s.commitSIWCImport(p, b.settings, created); err != nil {
		return nil, err
	}
	return created.SIWCTokens, nil
}

func (s *Server) commitSIWCImport(p model.MailAccountProfile, v model.ProSettings, created sub2.Account) (model.MailAccountProfile, error) {
	if created.SIWCTokens == nil || created.ID < 1 {
		return p, errors.New("SIWC 导入缺少已确认账号和凭证")
	}
	if err := s.store.SaveMailAccountSIWC(p.Email, *created.SIWCTokens); err != nil {
		return p, err
	}
	p, err := s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		completeProPushTarget(p, v)
		p.PushProvider, p.PushStatus = "sub2", "completed"
		p.Sub2AccountID, p.Sub2AccountName = created.ID, created.Name
		p.Sub2ProxyBindingID = created.ProxyBindingID
		ref := model.ProDownstream(v)
		p.ProPushDownstream = &ref
		p.QuotaCheckedAt = nil
		if p.ProQualitySince == nil {
			now := time.Now()
			p.ProQualitySince = &now
		}
		p.ProLastError = ""
		p.ProAuto.OAuthSource = "siwc_sub2api"
		if p.ProAuto.ID != "" {
			if p.ProAuto.Steps == nil {
				p.ProAuto.Steps = map[string]string{}
			}
			p.ProAuto.Steps["oauth"], p.ProAuto.Steps["push"] = "completed", "completed"
			if p.ProAuto.StageTimes == nil {
				p.ProAuto.StageTimes = map[string]time.Time{}
			}
			now := time.Now()
			for _, key := range []string{"oauth:completed", "push:completed"} {
				if _, exists := p.ProAuto.StageTimes[key]; !exists {
					p.ProAuto.StageTimes[key] = now
				}
			}
			p.ProAuto.UpdatedAt = now
		}
	})
	if err == nil {
		s.auditProEvent(p, "push", "completed", "sub2", "SIWC s2接口已完成授权并导入配置分组", map[string]any{"account_id": created.ID, "group_ids": v.Sub2.GroupIDs})
		s.completeSub2Push(v.Sub2, "pro-siwc:"+p.ID)
	}
	return p, err
}

// Resolve a response lost after import before starting any new authorization.
func (s *Server) recoverSIWCImport(ctx context.Context, p model.MailAccountProfile, v model.ProSettings, password string) (model.MailAccountProfile, bool, error) {
	endpoint, err := sub2.EndpointKey(v.Sub2.URL)
	if err != nil {
		return p, false, err
	}
	raw, err := s.store.Sub2PushAttempt(sub2PushAttemptID(endpoint, "pro-siwc:"+p.ID))
	if err != nil || raw == "" {
		return p, false, err
	}
	var attempt sub2PushAttempt
	if err = json.Unmarshal([]byte(raw), &attempt); err != nil {
		return p, true, err
	}
	if attempt.Input.SIWCImport == nil {
		return p, false, nil
	}
	created, err := s.createSub2WithProxy(ctx, v.Sub2, password, attempt.Input, "pro-siwc:"+p.ID, "pro-siwc-"+p.ID, nil)
	if err != nil {
		return p, true, err
	}
	p, err = s.commitSIWCImport(p, v, created)
	return p, true, err
}
