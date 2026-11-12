package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
	"chatgpt-space-merge/internal/workflow"
)

type proSIWCQuotaLoginKey struct{}

// Called inside the existing per-Pro-account lock. Only the quota step relogs;
// it cannot call payment, SIWC authorization or downstream account creation.
func (s *Server) queryProSIWCQuota(ctx context.Context, p model.MailAccountProfile, v model.ProSettings, password string) (sub2.QuotaUsage, error) {
	_, credentials, err := s.store.MailAccountCredential(p.Email)
	if err != nil {
		return sub2.QuotaUsage{}, err
	}
	c := credentials.SIWC
	if c == nil {
		return sub2.QuotaUsage{}, errors.New("账号缺少 SIWC 凭证，不能同步网页额度")
	}
	endpoint, err := sub2.EndpointKey(v.Sub2.URL)
	if err != nil {
		return sub2.QuotaUsage{}, err
	}
	expiry, validExpiry := workflow.AccessTokenExpiry(c.WebAccessToken)
	state := c.WebQuota
	needsLogin := c.WebAccessToken == "" || !validExpiry || expiry.Before(time.Now().Add(time.Minute))
	// One fresh login after purchase, regardless of the pre-purchase JWT exp.
	paidAt := p.ProAuto.StageTimes["recharge:completed"]
	if p.ProAuto.Steps["recharge"] == "completed" && (state == nil || state.LoginAt.IsZero() || !paidAt.IsZero() && state.LoginAt.Before(paidAt)) {
		needsLogin = true
	}
	pending := state != nil && !state.LoginAt.IsZero() && (!state.Synced || state.Endpoint != endpoint || state.AccountID != p.Sub2AccountID)
	if !needsLogin && !pending {
		q, e := s.sub2.QueryQuota(ctx, v.Sub2, password, p.Sub2AccountID)
		if !errors.Is(e, sub2.ErrSIWCWebUnauthorized) {
			return q, e
		}
		needsLogin = true
	}
	checkBinding := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		latest, _, err := s.store.MailAccountCredential(p.Email)
		if err != nil {
			return err
		}
		current, _, _, err := s.proAccountPushSettings(latest)
		if err != nil {
			return err
		}
		if !sameProQuotaBinding(p, latest) || !model.ProDownstream(v).Matches(model.ProDownstream(current)) || p.ProAuto.StopVersion != latest.ProAuto.StopVersion || (!p.ProAuto.StopRequested && latest.ProAuto.StopRequested) || latest.DispositionStarted() {
			return errors.New("账号流程或推送目标已变化，停止同步网页额度凭证")
		}
		return nil
	}
	if err = checkBinding(); err != nil {
		return sub2.QuotaUsage{}, err
	}
	if needsLogin {
		if state != nil && time.Now().Before(state.RetryAt) {
			return sub2.QuotaUsage{}, errors.New("网页额度凭证正在等待登录重试，保留原账号和额度")
		}
		next := time.Now().Add(time.Duration(max(60, v.Sub2.QuotaCheckIntervalSeconds)) * time.Second)
		if err = s.store.UpdateMailAccountSIWCWeb(p.Email, c.ClientID, func(current *model.SIWCCredentials) error {
			if current.WebQuota == nil {
				current.WebQuota = &model.SIWCWebQuotaState{}
			}
			current.WebQuota.RetryAt = next
			return nil
		}); err != nil {
			return sub2.QuotaUsage{}, err
		}
		s.auditProEvent(p, "quota", "running", "sub2", "重新登录获取付款后的网页 AT，仅更新额度查询凭证", nil)
		var result map[string]any
		if s.proAutoHooks != nil && s.proAutoHooks.QuotaWebLogin != nil {
			result, err = s.proAutoHooks.QuotaWebLogin(ctx, p.Email)
		} else {
			loginCtx, cancel := context.WithTimeout(context.WithValue(ctx, proSIWCQuotaLoginKey{}, true), 4*time.Minute)
			result, err = s.executeOpenAILoginContext(loginCtx, p.Email, "chatgpt_at", nil, nil)
			cancel()
		}
		if err != nil {
			return sub2.QuotaUsage{}, fmt.Errorf("额度步骤重新登录失败：%w", err)
		}
		if result["success"] != true {
			return sub2.QuotaUsage{}, errors.New("额度步骤重新登录未成功，保留原账号，等待下次重试")
		}
		token, _ := result["access_token"].(string)
		if token == "" {
			token, _ = result["accessToken"].(string)
		}
		info, e := workflow.DecodeUserInfo(token)
		expires, ok := workflow.AccessTokenExpiry(token)
		if e != nil || !strings.EqualFold(info.Email, p.Email) || info.AccountID == "" || (c.WebAccountID != "" && info.AccountID != c.WebAccountID) || !ok || !expires.After(time.Now().Add(time.Minute)) {
			return sub2.QuotaUsage{}, errors.New("重新登录的网页 AT 账号、空间或有效期校验失败")
		}
		if token == c.WebAccessToken {
			return sub2.QuotaUsage{}, errors.New("重新登录仍返回原网页 AT，未用旧凭证继续查额度")
		}
		if err = checkBinding(); err != nil {
			return sub2.QuotaUsage{}, err
		}
		if err = s.store.UpdateMailAccountSIWCWeb(p.Email, c.ClientID, func(current *model.SIWCCredentials) error {
			current.WebAccessToken, current.WebAccountID = token, info.AccountID
			current.WebQuota = &model.SIWCWebQuotaState{LoginAt: time.Now(), RetryAt: next, Endpoint: endpoint, AccountID: p.Sub2AccountID}
			return nil
		}); err != nil {
			return sub2.QuotaUsage{}, err
		}
	}
	if err = checkBinding(); err != nil {
		return sub2.QuotaUsage{}, err
	}
	_, credentials, err = s.store.MailAccountCredential(p.Email)
	if err != nil {
		return sub2.QuotaUsage{}, err
	}
	fresh := credentials.SIWC
	if fresh == nil || fresh.ClientID != c.ClientID {
		return sub2.QuotaUsage{}, errors.New("SIWC 授权已变化，停止同步网页凭证")
	}
	if err = s.sub2.UpdateSIWCWebCredentials(ctx, v.Sub2, password, p.Sub2AccountID, fresh.WebAccessToken, fresh.WebAccountID); err != nil {
		return sub2.QuotaUsage{}, fmt.Errorf("新网页 AT 已保存，同步原 s2 账号失败，下次重试同步：%w", err)
	}
	if err = checkBinding(); err != nil {
		return sub2.QuotaUsage{}, err
	}
	if err = s.store.UpdateMailAccountSIWCWeb(p.Email, c.ClientID, func(current *model.SIWCCredentials) error {
		if current.WebAccessToken != fresh.WebAccessToken {
			return errors.New("网页凭证已更新，丢弃旧同步结果")
		}
		if current.WebQuota != nil {
			current.WebQuota.Synced, current.WebQuota.Endpoint, current.WebQuota.AccountID = true, endpoint, p.Sub2AccountID
		}
		return nil
	}); err != nil {
		return sub2.QuotaUsage{}, err
	}
	s.auditProEvent(p, "quota", "running", "sub2", "新网页 AT 已同步原账号，重新查询 SIWC 额度", nil)
	return s.sub2.RefreshSIWCQuota(ctx, v.Sub2, password, p.Sub2AccountID)
}
