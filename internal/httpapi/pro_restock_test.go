package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestProRestockStoppedWorkerDoesNotRecover(t *testing.T) {
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected payment") })
	configureProAutoFixture(t, s)
	s.proAutoCancel()
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c, err := st.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := st.ReserveProRestock(restock.Decision{ID: "stop-test", At: time.Now(), Version: c.Version, Suggested: 1}, "", 0)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("reserve: %v %v", tasks, err)
	}
	_, err = st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "stoppable", Status: "interrupted", Steps: map[string]string{"oauth": "completed"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	s.proAutoHooks = &proAutomationHooks{Push: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return model.MailAccountProfile{}, ctx.Err()
	}}
	if rec := payCall(t, s.startProAutomation, "email", email, map[string]any{}); rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	if rec := payCall(t, s.stopProAutomation, "email", email, nil); rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	profile := waitProAutoFinished(t, s, email)
	if profile.ProAuto.Status != "stopped" || profile.ProAuto.NextCheckAt != nil {
		t.Errorf("user stop not durable: %+v", profile.ProAuto)
	}
	p, _, _, _ := st.ProSettings()
	_, _, err = s.restockTasks(restock.Snapshot{Binding: restockBinding(p), GroupID: 1}, p, c)
	if err != nil {
		t.Fatal(err)
	}
	if s.proAutomationRunning(email) {
		t.Error("restock restarted manually stopped worker")
	}
	// An explicit user resume clears the durable stop and uses the paid tail.
	if rec := payCall(t, s.startProAutomation, "email", email, map[string]any{}); rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("explicit resume did not start")
	}
	profile, _, err = st.MailAccountCredential(email)
	if err != nil || profile.ProAuto.StopRequested {
		t.Fatal("resume retained stop marker", err)
	}
	if rec := payCall(t, s.stopProAutomation, "email", email, nil); rec.Code != 202 {
		t.Fatal(rec.Body.String())
	}
	waitProAutoFinished(t, s, email)
}

func TestProRestockDispatchRejectsExpiredSnapshot(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected supplier request") })
	s.proAutoCancel()
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c, err := st.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	p, _, _, _ := st.ProSettings()
	pay, _, _ := st.GPTPaySettings()
	snap := restock.Snapshot{At: time.Now().Add(-time.Hour), Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity)}
	tasks, err := st.ReserveProRestock(restock.Decision{ID: "stale-dispatch", At: time.Now(), Version: c.Version, Suggested: 1, Snapshot: snap}, "", 0)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("reserve: %v %v", tasks, err)
	}
	s.dispatchProRestock(context.Background(), c, p, snap)
	tasks, err = st.ProRestockTasks()
	if err != nil || len(tasks) != 1 || tasks[0].Status != "reserved" || tasks[0].SubmittedAt != nil {
		t.Fatalf("expired snapshot dispatched a payment intent: %+v %v", tasks, err)
	}
}

func TestProRestockFullAutomationAndIndividualReady(t *testing.T) {
	var purchases, pushes atomic.Int32
	var failAuthorization atomic.Bool
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		purchases.Add(1)
		fmt.Fprint(w, `{"code":0,"data":{"id":"restock-order","status":"success"}}`)
	})
	configureProAutoFixture(t, s)
	s.proAutoCancel() // drive deterministic ticks; production uses the background monitor
	s.proAutoWG.Wait()
	settings, password, _, _ := st.ProSettings()
	settings.RetryCount, settings.RetryIntervalSeconds = 1, 1
	if _, err := st.SaveProSettings(settings, password, ""); err != nil {
		t.Fatal(err)
	}
	c := restock.DefaultConfig()
	c.Enabled = true
	c.Mode = "auto"
	c.GroupID = 1
	c.TimeEnabled = false
	c.Confirmations = 1
	c.MinBatch = 1
	c.MaxBatch = 1
	c, err := st.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	p, _, _, _ := st.ProSettings()
	pay, _, _ := st.GPTPaySettings()
	ready := false
	s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
		snap := restock.Snapshot{ID: "sample", At: time.Now(), Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Accounts: []restock.Account{{ID: 1, Ready: true, Capacity: 10, Used: 9}}}
		if ready {
			snap.Accounts = append(snap.Accounts, restock.Account{ID: 88, Ready: true, Capacity: 10})
		}
		return snap, nil
	}
	s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
		st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.ExitIP = "198.51.100.8" })
		if _, e := rpc("pro_recharge", map[string]any{"exit_ip": "198.51.100.8", "session": map[string]any{"access_token": proAutoToken(email, "initial"), "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}}}); e != nil {
			return e
		}
		if failAuthorization.CompareAndSwap(true, false) {
			return errors.New("lost login after confirmed payment")
		}
		_, e := rpc("pro_tokens", map[string]any{"exit_ip": "198.51.100.8", "tokens": map[string]any{"access_token": proAutoToken(email, "new"), "refresh_token": "new-rt"}})
		return e
	}, Push: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
		pushes.Add(1)
		return st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.PushStatus = "completed"
			p.PushProvider = "sub2"
			p.Sub2AccountID = 88
		})
	}, Quota: func(context.Context, string, bool) (model.MailAccountProfile, error) {
		return model.MailAccountProfile{}, errors.New("quota temporarily unavailable")
	}, Merge: func(context.Context, string) (model.MailAccountProfile, error) {
		t.Error("unexpected merge")
		return model.MailAccountProfile{}, nil
	}}
	failAuthorization.Store(true)
	s.tickProRestock(context.Background(), true)
	profile := waitProAutoFinished(t, s, email)
	// The configured recovery resumes authorization without another payment.
	if profile.ProAuto.Status != "waiting_quota" || purchases.Load() != 1 || pushes.Load() != 1 {
		t.Fatalf("full automation not used: %+v purchases=%d pushes=%d", profile.ProAuto, purchases.Load(), pushes.Load())
	}
	s.tickProRestock(context.Background(), true)
	tasks, _ := st.ProRestockTasks()
	if len(tasks) != 1 || tasks[0].Status != "verifying" {
		t.Fatalf("push response must not mean ready: %+v", tasks)
	}
	ready = true
	s.tickProRestock(context.Background(), true)
	tasks, _ = st.ProRestockTasks()
	if tasks[0].Status != "ready" || tasks[0].ReadyAt == nil {
		t.Fatalf("individual ready should not wait on quota: %+v", tasks)
	}
	for range 3 {
		s.tickProRestock(context.Background(), true)
	}
	if purchases.Load() != 1 || pushes.Load() != 1 {
		t.Fatal("duplicate execution")
	}
	before, _ := st.ProRestockSnapshot()
	s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
		return restock.Snapshot{}, errors.New("monitor unavailable")
	}
	s.tickProRestock(context.Background(), true)
	if s.proRestock.lastSnapshot.Valid || !s.proRestock.lastSnapshot.At.Equal(before.At) || len(s.proRestock.lastSnapshot.Accounts) != 2 || s.proRestock.decision.Suggested != 0 {
		t.Fatal("last valid data not preserved or used for ordering")
	}
	records, _ := st.ProRestockDecisions(10, 0)
	if len(records) < 1 {
		t.Fatal("missing durable record")
	}
	linked, _ := st.ProRestockDecisionTasks(tasks[0].DecisionID)
	if len(linked) != 1 {
		t.Fatal("missing record task link")
	}
	// Old fixed-count scheduler configuration remains unchanged.
	after, _, _, _ := st.ProSettings()
	a, _ := json.Marshal(p)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("restock changed legacy settings")
	}
}

func TestProRestockUnknownPaymentRetainsOriginalOrder(t *testing.T) {
	var purchases atomic.Int32
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		purchases.Add(1)
		w.WriteHeader(503)
		fmt.Fprint(w, `{"code":50301}`)
	})
	configureProAutoFixture(t, s)
	s.proAutoCancel()
	c := restock.DefaultConfig()
	c.Enabled = true
	c.Mode = "auto"
	c.GroupID = 1
	c.TimeEnabled = false
	c.Confirmations = 1
	c.MinBatch = 1
	c.MaxBatch = 1
	c.UnitCost = 25
	c, err := st.SaveProRestockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	p, _, _, _ := st.ProSettings()
	pay, _, _ := st.GPTPaySettings()
	s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
		return restock.Snapshot{At: time.Now(), Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Accounts: []restock.Account{{ID: 1, Ready: true, Capacity: 10, Used: 9}}}, nil
	}
	s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
		st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.ExitIP = "198.51.100.8" })
		_, e := rpc("pro_recharge", map[string]any{"exit_ip": "198.51.100.8", "session": map[string]any{"access_token": proAutoToken(email, "initial"), "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}}})
		return e
	}}
	s.tickProRestock(context.Background(), true)
	profile := waitProAutoFinished(t, s, email)
	original := profile.ProAuto.OrderID
	if original == "" {
		t.Fatal("missing original order")
	}
	// Crash window: order committed but profile linkage was not saved.
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.OrderID = "" })
	for range 3 {
		s.tickProRestock(context.Background(), true)
	}
	tasks, _ := st.ProRestockTasks()
	limits, _ := st.ProRestockLimits(time.Now())
	if len(tasks) != 1 || !tasks[0].AutoIsolated || tasks[0].Active() || tasks[0].OrderID != original || tasks[0].ETA != nil || limits.DayCost != 25 || purchases.Load() != 1 {
		t.Fatalf("unknown order lost or repaid: %+v %+v purchases=%d", tasks, limits, purchases.Load())
	}
}
