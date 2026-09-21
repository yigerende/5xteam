package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProSchedulerBatchCapacityQuotaFailureAndNoOverlap(t *testing.T) {
	var purchases, logins atomic.Int32
	var failQuota atomic.Bool
	failQuota.Store(true)
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		purchases.Add(1)
		var input struct {
			Session struct {
				User struct {
					Email string `json:"email"`
				} `json:"user"`
			} `json:"session"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		fmt.Fprintf(w, `{"code":0,"data":{"id":%q,"status":"success"}}`, input.Session.User.Email)
	})
	configureProAutoFixture(t, s)
	cfg, _, _, _ := st.ProSettings()
	cfg.MaxUnmerged = 5
	cfg.Concurrency = 2
	st.SaveProSettings(cfg, "", "")
	for i := 0; i < 4; i++ {
		e := fmt.Sprintf("paid%d@example.com", i)
		st.SaveMailAccount(model.MailAccountProfile{Email: e}, model.MailAccountCredentials{Email: e})
		st.UpdateMailAccountManagementScope(e, "pro")
		st.UpdateProAccount(e, func(p *model.MailAccountProfile) { p.CurrentPlanType = "pro" })
	}
	for i := 0; i < 10; i++ {
		e := fmt.Sprintf("fresh%d@example.com", i)
		st.SaveMailAccount(model.MailAccountProfile{Email: e}, model.MailAccountCredentials{Email: e})
		st.UpdateMailAccountManagementScope(e, "pro")
	}
	s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
		if _, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.ExitIP = "198.51.100.8" }); err != nil {
			return err
		}
		logins.Add(1)
		_, err := rpc("pro_recharge", map[string]any{"exit_ip": "198.51.100.8", "session": map[string]any{"access_token": proAutoToken(email, "initial"), "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}}})
		if err != nil {
			return err
		}
		_, err = rpc("pro_tokens", map[string]any{"exit_ip": "198.51.100.8", "tokens": map[string]any{"access_token": proAutoToken(email, "new"), "refresh_token": "new-rt"}})
		return err
	}, Push: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
		return st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.PushStatus = "completed"
			p.PushProvider = "sub2"
			p.Sub2AccountID = 88
		})
	}, Quota: func(ctx context.Context, email string, _ bool) (model.MailAccountProfile, error) {
		if failQuota.Load() {
			return model.MailAccountProfile{}, errors.New("fixture quota cooling")
		}
		return st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 20}
			p.QuotaStatus = "completed"
		})
	}, Merge: func(context.Context, string) (model.MailAccountProfile, error) {
		t.Error("low quota must not merge")
		return model.MailAccountProfile{}, nil
	}}
	// Many simultaneous manual ticks must claim a single one-account batch.
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.tickProSchedule(true) }()
	}
	wg.Wait()
	p := waitProAutoFinished(t, s, email)
	if p.ProAuto.Status != "waiting_quota" {
		t.Fatalf("unexpected status %+v", p.ProAuto)
	}
	run, err := st.ActiveProScheduleRun()
	if err != nil || run == nil || len(run.Tasks) != 1 || purchases.Load() != 1 {
		t.Fatalf("wrong bounded batch %+v %v purchases=%d", run, err, purchases.Load())
	}
	// Even with new capacity, failed initial quota holds this batch open.
	st.UpdateProAccount("paid0@example.com", func(p *model.MailAccountProfile) { p.SpaceMergedOnce = true })
	s.tickProSchedule(true)
	if logins.Load() != 1 {
		t.Fatal("second batch started before first quota success")
	}
	failQuota.Store(false)
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) { past := time.Now().Add(-time.Second); p.ProAuto.NextCheckAt = &past })
	s.queueDueProAutomation(email)
	p = waitProAutoFinished(t, s, email)
	if p.ProAuto.ProvisionedAt == nil || p.ProAuto.Status != "waiting_quota" {
		t.Fatal("successful initial quota did not finish provisioning")
	}
	s.tickProSchedule(false)
	if run, err = st.ActiveProScheduleRun(); err != nil || run != nil {
		t.Fatal("completed batch stayed active", err)
	}
	runs, _, _ := st.ProScheduleRuns(10, 0)
	if len(runs) < 1 || runs[0].Status != "completed" || runs[0].Tasks[0].Status != "completed" {
		t.Fatal("missing completed execution record")
	}
	if purchases.Load() != 1 || logins.Load() != 1 {
		t.Fatal("quota retry repeated purchase/login")
	}
}

func TestProSchedulerDisabledAndNoCards(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected payment") })
	configureProAutoFixture(t, s)
	s.tickProSchedule(false)
	runs, total, err := st.ProScheduleRuns(10, 0)
	if err != nil || total != 0 || len(runs) != 0 {
		t.Fatal("disabled timer ran")
	}
	st.DeleteGPTPayCard("test-card")
	s.tickProSchedule(true)
	runs, _, _ = st.ProScheduleRuns(10, 0)
	if len(runs) != 1 || runs[0].Status != "skipped" {
		t.Fatal("no-card run missing")
	}
}
