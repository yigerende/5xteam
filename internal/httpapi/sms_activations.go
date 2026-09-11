package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/herosms"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func (s *Server) heroEvent(email, id, event, message string, err error) {
	level := "info"
	detail := map[string]any{"activation_id": id, "event": event}
	status := 0
	var request map[string]any
	if err != nil {
		level = "warning"
		detail["error"] = err.Error()
		var apiErr *herosms.Error
		if errors.As(err, &apiErr) {
			status = apiErr.Status
			request = map[string]any{"method": apiErr.Method, "path": apiErr.Path}
			detail["provider_response"] = apiErr.Detail
		}
	}
	s.enqueueAuditEvent(model.AutoRotationEvent{AccountID: s.store.SMSAccountID(email), Email: email, Type: "sms_activation", Source: "oauth", Operation: "oauth", Stage: "phone_pool", Provider: "hero_sms", Message: message, Details: detail, Request: request, Level: level, HTTPStatus: status})
}

func (s *Server) heroConfig() (herosms.Config, error) {
	raw, err := s.store.SMSConfigRaw("hero_sms")
	if err != nil {
		return herosms.Config{}, err
	}
	str := func(k string) string {
		v := raw[k]
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	if raw["enabled"] != true {
		return herosms.Config{}, errors.New("Hero-SMS 未启用")
	}
	return herosms.Config{BaseURL: str("base_url"), APIKey: str("api_key"), Service: str("service"), Country: str("country"), MaxPrice: str("max_price")}, nil
}

func (p *oauthProtocolRPC) callHero(method string, args map[string]any) (any, error) {
	id, _ := args["id"].(string)
	if method == "hero_acquire" || method == "hero_replace" {
		gate, _ := p.server.heroAccountLocks.LoadOrStore(strings.ToLower(p.email), &sync.Mutex{})
		gate.(*sync.Mutex).Lock()
		defer gate.(*sync.Mutex).Unlock()
		var cfg herosms.Config
		var activation herosms.Activation
		var err error
		if method == "hero_acquire" {
			outstanding, checkErr := p.server.store.HasActiveSMSActivation(p.email)
			if checkErr != nil {
				return nil, checkErr
			}
			if outstanding {
				return nil, errors.New("该账号的上一个 Hero 激活尚未回收，暂不购买新号码")
			}
			// A failed release must not allow a second live activation in this job.
			if len(p.heroOwned) > 0 {
				return nil, errors.New("本次任务已有 Hero 激活，禁止重复申请")
			}
			cfg, err = p.server.heroConfig()
			if err != nil {
				return nil, err
			}
			activation, err = herosms.Acquire(p.ctx, cfg)
		} else {
			var owned bool
			cfg, owned = p.heroOwned[id]
			if !owned {
				return nil, errors.New("任务不拥有此 Hero 激活")
			}
			activation, err = herosms.Replace(p.ctx, cfg, id)
		}
		if err != nil {
			p.server.heroEvent(p.email, id, method+"_failed", "Hero 申请或换号失败，未继续购买新号", err)
			return nil, err
		}
		newID := activation.ID.String()
		if p.heroOwned == nil {
			p.heroOwned = map[string]herosms.Config{}
		}
		p.heroOwned[newID] = cfg
		if err = p.server.store.SaveSMSActivation(newID, p.email, cfg); err != nil {
			// Do not leave an untracked billable activation when SQLite fails.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cleanupErr := herosms.Release(cleanupCtx, cfg, newID, false)
			p.server.heroEvent(p.email, newID, "track_failed", "Hero 激活登记失败，已尝试立即取消", cleanupErr)
			return nil, err
		}
		if method == "hero_replace" && id != newID {
			if err = p.server.store.UpdateSMSActivation(id, "replaced", "", 0, time.Time{}); err != nil {
				return nil, err
			}
			delete(p.heroOwned, id)
		}
		p.server.heroEvent(p.email, newID, method, "Hero 激活已登记到本次任务", nil)
		return map[string]any{"id": newID, "phone": activation.Phone, "expired_at": activation.ExpiredAt}, nil
	}
	cfg, owned := p.heroOwned[id]
	if !owned {
		return nil, errors.New("任务不拥有此 Hero 激活")
	}
	switch method {
	case "hero_fetch":
		wait := 10 * time.Second
		if seconds, ok := args["timeout_seconds"].(float64); ok && seconds > 0 && seconds < 10 {
			wait = time.Duration(seconds * float64(time.Second))
		}
		ctx, cancel := context.WithTimeout(p.ctx, wait)
		defer cancel()
		code, err := herosms.Fetch(ctx, cfg, id)
		if err != nil {
			var apiErr *herosms.Error
			if errors.As(err, &apiErr) && apiErr.Status == 404 {
				return map[string]any{"found": false, "message": "等待短信，暂无 OTP"}, nil
			}
			return nil, err
		}
		return map[string]any{"found": code != "", "code": code}, nil
	case "hero_release":
		finish, _ := args["finish"].(bool)
		if err := p.server.store.QueueSMSActivation(id, finish); err != nil {
			return nil, err
		}
		delete(p.heroOwned, id)
		p.server.heroEvent(p.email, id, "cleanup_queued", "Hero 回收已排队，等待平台确认", nil)
		return true, nil
	}
	return nil, errors.New("不支持的 Hero 操作")
}

func (s *Server) cleanupHeroActivation(ctx context.Context, item store.SMSActivation) {
	finish := item.State == "finish_pending"
	err := herosms.Release(ctx, item.Config, item.ID, finish)
	var denied *herosms.Error
	if !finish && errors.As(err, &denied) && denied.Status == 422 {
		// Received OTPs are not refundable. Finish only after confirming an OTP exists.
		if code, otpErr := herosms.Fetch(ctx, item.Config, item.ID); otpErr == nil && code != "" {
			if saveErr := s.store.UpdateSMSActivation(item.ID, "finish_pending", "已收到短信，平台不允许取消退款", item.Attempts, time.Now()); saveErr != nil {
				return
			}
			finish = true
			item.State = "finish_pending"
			s.heroEvent(item.Email, item.ID, "finish_required", "Hero 已收码不支持取消退款，改为完成激活", nil)
			err = herosms.Release(ctx, item.Config, item.ID, true)
		}
	}
	// A 404 is only terminal after the active list confirms this ID is absent.
	var apiErr *herosms.Error
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		absent := false
		for page := 1; page <= 100; page++ {
			result, listErr := herosms.Active(ctx, item.Config, page)
			if listErr != nil {
				break
			}
			rows, valid := result["data"].([]any)
			if !valid {
				break
			}
			found := false
			for _, row := range rows {
				if obj, ok := row.(map[string]any); ok && fmt.Sprint(obj["id"]) == item.ID {
					found = true
					break
				}
			}
			if found {
				break
			}
			if len(rows) < 25 {
				absent = true
				break
			}
		}
		if absent {
			err = nil
		}
	}
	state, message := item.State, "Hero 回收失败，稍后重试"
	lastError := ""
	attempts := item.Attempts + 1
	next := time.Now().Add(time.Duration(min(120, 15*attempts)) * time.Second)
	if err == nil {
		state, message = "cancelled", "Hero 取消已确认（或已不在活动列表）"
		if finish {
			state, message = "finished", "Hero 完成已确认（或已不在活动列表）"
		}
	} else {
		lastError = err.Error()
		if attempts >= 10 {
			state, message = "cleanup_failed", "Hero 回收重试已达上限，请检查平台激活"
		}
	}
	if saveErr := s.store.UpdateSMSActivation(item.ID, state, lastError, attempts, next); saveErr != nil {
		s.heroEvent(item.Email, item.ID, "cleanup_store_error", "Hero 回收状态保存失败", saveErr)
	}
	s.heroEvent(item.Email, item.ID, state, message+fmt.Sprintf("，第 %d 次", attempts), err)
}

func (s *Server) monitorHeroActivations(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		items, err := s.store.DueSMSActivations(time.Now())
		if err != nil {
			continue
		}
		for _, item := range items {
			if ctx.Err() != nil {
				return
			}
			requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			s.cleanupHeroActivation(requestCtx, item)
			cancel()
		}
	}
}

func (s *Server) getSMSActiveActivations(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.heroConfig()
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	result, err := herosms.Active(ctx, cfg, page)
	if err != nil {
		writeAPI(w, 502, nil, err.Error())
		return
	}
	writeAPI(w, 200, result, "")
}
