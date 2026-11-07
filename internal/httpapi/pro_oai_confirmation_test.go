package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestProOAIConfirmationSettings(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("settings must never send upstream requests") })
	pro := model.DefaultProSettings()
	for i, mode := range model.ProDispositionModes {
		pro.OAIConfirmation[mode] = model.ProOAIConfirmationRule{Enabled: true, DelaySeconds: 300 + i, RetryIntervalSeconds: 400 + i, MaxAttempts: 2 + i}
	}
	input := map[string]any{"automation": pro, "gptpay": map[string]any{"plan_code": "pro5"}}
	if rec := payCall(t, s.saveProAutomationSettings, "", "", input); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _, _, err := st.ProSettings()
	if err != nil {
		t.Fatal(err)
	}
	for i, mode := range model.ProDispositionModes {
		if saved.OAIConfirmation[mode] != (model.ProOAIConfirmationRule{Enabled: true, DelaySeconds: 300 + i, RetryIntervalSeconds: 400 + i, MaxAttempts: 2 + i}) {
			t.Fatal("lost independent setting", mode)
		}
	}
	raw, _ := json.Marshal(pro)
	var legacy map[string]any
	if err = json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "oai_confirmation")
	input["automation"] = legacy
	if rec := payCall(t, s.saveProAutomationSettings, "", "", input); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _, _, _ = st.ProSettings()
	if !saved.OAIConfirmation["merge"].Enabled || saved.OAIConfirmation["merchant"].DelaySeconds != 302 {
		t.Fatal("old client reset new settings")
	}
	for _, seconds := range []int{-1, 0, 604801} {
		pro.OAIConfirmation["merge"] = model.ProOAIConfirmationRule{Enabled: true, DelaySeconds: seconds}
		input["automation"] = pro
		if rec := payCall(t, s.saveProAutomationSettings, "", "", input); rec.Code != 400 {
			t.Fatal("invalid delay saved", seconds, rec.Code)
		}
	}
}

func oaiFixture(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream") })
	s.proAutoCancel()
	s.proAutoWG.Wait()
	cfg := model.DefaultProSettings()
	cfg.OAIConfirmation["merge_v2"] = model.ProOAIConfirmationRule{Enabled: true, DelaySeconds: 1, RetryIntervalSeconds: 1, MaxAttempts: 3}
	if _, err := st.SaveProSettings(cfg, "", ""); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	if _, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProMergeV2 = &model.ProMergeV2{Status: "completed", CompletedAt: &past}
		v := model.NewProOAIConfirmation("merge_v2", cfg.OAIConfirmation["merge_v2"])
		v.CompletedAt = past
		v.NotBefore = &past
		v.Status = "waiting"
		p.ProOAIConfirmations = map[string]*model.ProOAIConfirmation{"merge_v2": v}
	}); err != nil {
		t.Fatal(err)
	}
	return s, st, email
}

func oaiRecord(t *testing.T, st *store.Store, email string) *model.ProOAIConfirmation {
	t.Helper()
	p, _, err := st.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	return p.ProOAIConfirmations["merge_v2"]
}
func oaiHook(t *testing.T, answer string) func(context.Context, string) (map[string]any, error) {
	return func(ctx context.Context, email string) (map[string]any, error) {
		t.Helper()
		o := ctx.Value(proOAILoginKey{}).(*proOAILoginOptions)
		for _, v := range []struct {
			method string
			args   map[string]any
		}{
			{"help_stage", map[string]any{"stage": "login", "status": "completed"}},
			{"help_stage", map[string]any{"stage": "authorize", "status": "completed"}},
			{"help_sending", nil},
			{"help_thread", map[string]any{"conversation_id": "thread-1", "user_message_id": "user-1"}},
			{"help_reply", map[string]any{"conversation_id": "thread-1", "message_id": "assistant-1", "answer": answer}},
		} {
			if _, err := o.Call(v.method, v.args); err != nil {
				return nil, err
			}
		}
		return map[string]any{"success": true}, nil
	}
}

func TestProOAISuccessTerminalAndConcurrentClaim(t *testing.T) {
	s, st, email := oaiFixture(t)
	var calls atomic.Int32
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	s.proOAILogin = func(ctx context.Context, email string) (map[string]any, error) {
		calls.Add(1)
		close(entered)
		<-release
		return oaiHook(t, "该订阅退款已发起。\n已成功")(ctx, email)
	}
	go func() { done <- s.runProOAI(context.Background(), email, "merge_v2") }()
	<-entered
	if err := s.runProOAI(context.Background(), email, "merge_v2"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	v := oaiRecord(t, st, email)
	if v.Status != "confirmed" || v.ConfirmedAt == nil || len(v.Replies) != 1 || v.NextAttemptAt != nil {
		t.Fatalf("bad final %+v", v)
	}
	if err := s.runProOAI(context.Background(), email, "merge_v2"); err != nil || calls.Load() != 1 {
		t.Fatal("confirmed task repeated", err)
	}
	if rec := payCall(t, s.manageProOAI, "email", email, map[string]string{"mode": "merge_v2", "action": "retry"}); rec.Code != 409 {
		t.Fatal("confirmed retry allowed", rec.Code)
	}
}

func TestProOAINotDueDisabledOrStopped(t *testing.T) {
	s, st, email := oaiFixture(t)
	s.proOAILogin = func(context.Context, string) (map[string]any, error) { t.Error("must not login"); return nil, nil }
	future := time.Now().Add(time.Hour)
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProOAIConfirmations["merge_v2"].NotBefore = &future })
	s.runProOAI(context.Background(), email, "merge_v2")
	past := time.Now().Add(-time.Hour)
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProOAIConfirmations["merge_v2"].NotBefore = &past
		p.ProAuto.StopRequested = true
	})
	s.runProOAI(context.Background(), email, "merge_v2")
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = false })
	cfg, _, _, _ := st.ProSettings()
	r := cfg.OAIConfirmation["merge_v2"]
	r.Enabled = false
	cfg.OAIConfirmation["merge_v2"] = r
	st.SaveProSettings(cfg, "", "")
	s.runProOAI(context.Background(), email, "merge_v2")
	if oaiRecord(t, st, email).Attempts != 0 {
		t.Fatal("inactive task claimed")
	}
}

func TestProOAIProducedRefundPersistsConfirmation(t *testing.T) {
	s, st, email := oaiFixture(t)
	answer := "系统记录显示账户（" + email + "）该笔款项**已产生退款**，预计 5–10 个工作日入账。\n\n已成功"
	s.proOAILogin = oaiHook(t, answer)
	if err := s.runProOAI(context.Background(), email, "merge_v2"); err != nil {
		t.Fatal(err)
	}
	v := oaiRecord(t, st, email)
	if v.Status != "confirmed" || v.ConfirmedAt == nil || v.ConfirmationSource != "help" || v.Steps["confirm"] != "confirmed" || v.NextAttemptAt != nil {
		t.Fatalf("refund answer did not complete confirmation: %+v", v)
	}
	if v.Answer != answer || len(v.Replies) != 1 || v.Replies[0].Answer != answer || v.Replies[0].Status != "confirmed" {
		t.Fatalf("original answer or result lost: %+v", v)
	}
	s.proOAILogin = func(context.Context, string) (map[string]any, error) {
		t.Fatal("confirmed reply must not trigger another inquiry")
		return nil, nil
	}
	if err := s.runProOAI(context.Background(), email, "merge_v2"); err != nil {
		t.Fatal(err)
	}
}

func TestProOAINegativeAndUnknownAnswersRetry(t *testing.T) {
	for _, answer := range []string{"没有退款。\n已失败", "无法查询。\n待核实"} {
		t.Run(answer, func(t *testing.T) {
			s, st, email := oaiFixture(t)
			s.proOAILogin = oaiHook(t, answer)
			if err := s.runProOAI(context.Background(), email, "merge_v2"); err != nil {
				t.Fatal(err)
			}
			v := oaiRecord(t, st, email)
			if (v.Status != "unconfirmed" && v.Status != "review") || v.NextAttemptAt == nil {
				t.Fatal("negative not scheduled")
			}
			past := time.Now().Add(-time.Minute)
			st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProOAIConfirmations["merge_v2"].NextAttemptAt = &past })
			s.proOAILogin = func(ctx context.Context, email string) (map[string]any, error) {
				o := ctx.Value(proOAILoginKey{}).(*proOAILoginOptions)
				if o.Config["sent_at"] != nil || o.Config["conversation_id"] != "thread-1" {
					t.Fatal("follow-up lost conversation or retained previous intent")
				}
				return oaiHook(t, "退款已处理。\n已成功")(ctx, email)
			}
			if err := s.runProOAI(context.Background(), email, "merge_v2"); err != nil {
				t.Fatal(err)
			}
			if v = oaiRecord(t, st, email); v.Status != "confirmed" || v.Attempts != 2 || len(v.Replies) != 2 {
				t.Fatal("follow-up failed", v)
			}
		})
	}
}

func TestProOAIStopRejectsLateReplyAndSession(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmation", true: "all"}[all], func(t *testing.T) {
			s, st, email := oaiFixture(t)
			s.proOAILogin = func(ctx context.Context, email string) (map[string]any, error) {
				o := ctx.Value(proOAILoginKey{}).(*proOAILoginOptions)
				if _, err := o.Call("help_sending", nil); err != nil {
					t.Fatal(err)
				}
				o.Call("help_thread", map[string]any{"conversation_id": "thread-1", "user_message_id": "user-1"})
				if all {
					payCall(t, s.stopProAutomation, "email", email, nil)
				} else {
					payCall(t, s.manageProOAI, "email", email, map[string]string{"mode": "merge_v2", "action": "stop"})
				}
				if _, err := o.Call("help_reply", map[string]any{"conversation_id": "thread-1", "message_id": "answer-1", "answer": "退款已处理。\n已成功"}); err == nil {
					t.Fatal("late reply accepted")
				}
				if _, err := o.Call("help_session", map[string]any{"session": map[string]any{"email": email, "cookies": "secret"}}); err == nil {
					t.Fatal("late session accepted")
				}
				return nil, errors.New("cancelled")
			}
			s.runProOAI(context.Background(), email, "merge_v2")
			v := oaiRecord(t, st, email)
			if v.Status != "stopped" || v.NextAttemptAt != nil || v.ConfirmedAt != nil {
				t.Fatal("stop overwritten", v)
			}
			saved, _ := st.ProOAISession(email, "merge_v2")
			if saved != nil {
				t.Fatal("late session persisted")
			}
		})
	}
}

func TestProOAIInterruptedFinalAttemptOnlyRecoversOriginal(t *testing.T) {
	s, st, email := oaiFixture(t)
	stamp := time.Now().Add(-time.Minute)
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		v := p.ProOAIConfirmations["merge_v2"]
		v.Status = "running"
		v.Attempts = 3
		v.SentAt = &stamp
		v.ConversationID = "original"
		v.UserMessageID = "original-user"
		v.RequestID = "original-request"
	})
	if err := st.RecoverProOAIConfirmations(); err != nil {
		t.Fatal(err)
	}
	s.proOAILogin = func(ctx context.Context, email string) (map[string]any, error) {
		o := ctx.Value(proOAILoginKey{}).(*proOAILoginOptions)
		if o.Config["sent_at"] == nil || o.Config["conversation_id"] != "original" || o.Config["request_id"] != "original-request" {
			t.Fatal("lost durable send intent")
		}
		if _, err := o.Call("help_sending", nil); err == nil {
			t.Fatal("duplicate send admitted")
		}
		_, err := o.Call("help_reply", map[string]any{"conversation_id": "original", "message_id": "answer", "answer": "退款已处理。\n已成功"})
		return map[string]any{"success": true}, err
	}
	if err := s.runProOAI(context.Background(), email, "merge_v2"); err != nil {
		t.Fatal(err)
	}
	if v := oaiRecord(t, st, email); v.Status != "confirmed" || v.Attempts != 3 {
		t.Fatal("last interrupted attempt did not finish", v)
	}
}

func TestProOAIDeletionPreparesWithoutSending(t *testing.T) {
	s, st, email := oaiFixture(t)
	cfg, _, _, _ := st.ProSettings()
	r := cfg.OAIConfirmation["deactivate"]
	r.Enabled = true
	cfg.OAIConfirmation["deactivate"] = r
	st.SaveProSettings(cfg, "", "")
	ctx, err := s.proOAIDeletionContext(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	o := ctx.Value(proOAILoginKey{}).(*proOAILoginOptions)
	if o.Config["prepare_only"] != true {
		t.Fatal("not prepare only")
	}
	if _, err = o.Call("help_sending", nil); err == nil {
		t.Fatal("deletion preparation sent message")
	}
	if _, err = o.Call("help_session", map[string]any{"session": map[string]any{"email": email, "proxy": "http://proxy.invalid", "cookies": []string{"private-cookie"}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProDeactivation = &model.ProDeactivation{Status: "completed", SubmittedAt: &now, AcceptedAt: &now, ConfirmedAt: &now}
	})
	if err != nil {
		t.Fatal(err)
	}
	v := p.ProOAIConfirmations["deactivate"]
	if v.Status != "waiting" || v.PreparedAt == nil || v.NotBefore == nil || v.NotBefore.Before(now.Add(299*time.Second)) {
		t.Fatal("completion lost prepared session", v)
	}
}

func TestProOAIManualHistoryAndAuthentication(t *testing.T) {
	s, st, email := oaiFixture(t)
	cfg, _, _, _ := st.ProSettings()
	rule := cfg.OAIConfirmation["merge_v2"]
	rule.Enabled = false
	cfg.OAIConfirmation["merge_v2"] = rule
	st.SaveProSettings(cfg, "", "")
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) { delete(p.ProOAIConfirmations, "merge_v2") })
	if rec := payCall(t, s.manageProOAI, "email", email, map[string]string{"mode": "merge_v2", "action": "retry"}); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if v := oaiRecord(t, st, email); v == nil || v.Status != "waiting" {
		t.Fatal("manual catchup missing")
	}
	s.proOAILogin = oaiHook(t, "退款已处理。\n已成功")
	if err := s.runProOAI(context.Background(), email, "merge_v2"); err != nil {
		t.Fatal(err)
	}
	if v := oaiRecord(t, st, email); v.Status != "confirmed" || !v.Manual {
		t.Fatal("manual confirmation blocked by automatic switch")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/pro-accounts/"+email+"/oai-confirmation", nil))
	if rec.Code != 401 {
		t.Fatal("anonymous confirmation action accessible", rec.Code)
	}
}
