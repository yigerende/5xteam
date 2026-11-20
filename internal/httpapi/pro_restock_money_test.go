package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func TestRestockMoneyUsageOpenAIWindows(t *testing.T) {
	now := time.Now()
	raw := fmt.Sprintf(`{"updated_at":%q,"five_hour":{"utilization":0,"resets_at":%q,"window_stats":{"cost":349.07}},"seven_day":{"utilization":67,"resets_at":%q,"window_stats":{"cost":348.51,"user_cost":69.7}}}`, now.Format(time.RFC3339Nano), now.Add(-time.Second).Format(time.RFC3339Nano), now.Add(7*24*time.Hour).Format(time.RFC3339Nano))
	var u sub2.MoneyUsage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	u.Day = now.UTC().Format("2006-01-02")
	m := restockMoneyUsage(u, now)
	if m.Error != "" || len(m.Windows) != 1 || m.Windows[0].Seconds != 604800 || m.Windows[0].UsedUSD != 348.51 {
		t.Fatalf("inactive 5h placeholder or user deductions affected account budget: %+v", m)
	}
	u.UpdatedAt = nil
	if restockMoneyUsage(u, now).Error == "" {
		t.Fatal("missing upstream timestamp trusted")
	}
	u.UpdatedAt = &now
	u.SevenDay.Stats.Cost = nil
	if restockMoneyUsage(u, now).Error == "" {
		t.Fatal("missing account cost trusted")
	}
}

func TestRestockMoneySamplesBetweenLongConcurrencyChecks(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture"}}`)
			return
		}
		if r.URL.Path == "/api/v1/admin/accounts/1/usage" {
			calls.Add(1)
			fmt.Fprintf(w, `{"code":0,"data":{"updated_at":%q}}`, time.Now().Format(time.RFC3339Nano))
			return
		}
		if r.URL.Path == "/api/v1/admin/usage/stats" {
			fmt.Fprint(w, `{"code":0,"data":{"total_account_cost":40}}`)
			return
		}
		t.Errorf("unexpected call %s", r.URL.Path)
	}))
	defer upstream.Close()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, _, _, err := st.ProSettings()
	if err != nil {
		t.Fatal(err)
	}
	p.Provider = "sub2"
	p.Sub2 = model.Sub2Settings{URL: upstream.URL, Email: "fixture@example.com", GroupIDs: []int64{1}, AccountConcurrency: 10}
	p, err = st.SaveProSettings(p, "fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, sub2: sub2.New()}
	now := time.Now()
	c := restock.DefaultConfig()
	c.Enabled, c.GroupID, c.CheckSeconds, c.FreshSeconds = true, 1, 300, 300
	s.proRestock.quotaBinding = restockBinding(p)
	s.proRestock.quotaNext = now.Add(time.Hour)
	s.proRestock.lastSnapshot = restock.Snapshot{Binding: restockBinding(p), GroupID: 1, At: now.Add(-time.Minute), Valid: true, LoadValid: true, Accounts: []restock.Account{{ID: 1, Ready: true}}}
	s.prepareRestockMoney("fixture", &s.proRestock.lastSnapshot)
	s.sampleRestockMoneyBetweenChecks(c, now)
	awaitMoneyWorker(t, s)
	if calls.Load() != 1 {
		t.Fatal("long concurrency interval prevented minute cost sampling")
	}
	s.proRestock.moneyNext = time.Time{}
	s.proRestock.lastSnapshot.At = now.Add(-time.Hour)
	s.sampleRestockMoneyBetweenChecks(c, now)
	awaitMoneyWorker(t, s)
	if calls.Load() != 1 {
		t.Fatal("stale membership reused for background sampling")
	}
}

func awaitMoneyWorker(t *testing.T, s *Server) {
	t.Helper()
	done := make(chan struct{})
	go func() { s.proAutoWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("money worker did not finish")
	}
}

func TestRestockMoneyRefreshCadenceFailureAndRecovery(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture"}}`)
		case "/api/v1/admin/accounts/1/usage":
			calls.Add(1)
			if fail.Load() {
				http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"updated_at":%q,"seven_day":{"utilization":50,"resets_at":%q,"window_stats":{"cost":100}}}}`, time.Now().Format(time.RFC3339Nano), time.Now().Add(time.Hour).Format(time.RFC3339Nano))
		case "/api/v1/admin/usage/stats":
			fmt.Fprint(w, `{"code":0,"data":{"total_account_cost":40}}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	now := time.Now()
	p := model.ProSettings{Provider: "sub2", Sub2: model.Sub2Settings{URL: upstream.URL, Email: "fixture@example.com", GroupIDs: []int64{1}}}
	s := &Server{sub2: sub2.New()}
	s.proRestock.quotaBinding = restockBinding(p)
	s.proRestock.quotaNext = now.Add(time.Hour)
	previous := &restock.AccountMoney{At: now.Add(-time.Minute), Day: now.UTC().Format("2006-01-02"), Samples: []restock.MoneySample{{At: now.Add(-3 * time.Minute), Cost: 10}, {At: now.Add(-2 * time.Minute), Cost: 20}, {At: now.Add(-time.Minute), Cost: 30}}}
	snap := restock.Snapshot{Binding: restockBinding(p), Accounts: []restock.Account{{ID: 1, Ready: true, Shared: true, Money: previous}, {ID: 2, Ready: false}}}
	c := restock.DefaultConfig()
	c.MoneyWindowMinutes = 1
	s.prepareRestockMoney("fixture", &snap)
	s.proRestock.money[1] = previous
	snap.Accounts[0].Money = previous
	s.refreshRestockMoney(c, p, "fixture", snap)
	awaitMoneyWorker(t, s)
	m := s.proRestock.money[1]
	if m == nil || m.Error != "" || m.RateUSD == nil || math.Abs(*m.RateUSD-10) > .1 || m.WindowMinutes != 1 || calls.Load() != 1 {
		t.Fatalf("independent sampling or cost delta failed: %+v; calls=%d", m, calls.Load())
	}
	s.refreshRestockMoney(restock.DefaultConfig(), p, "fixture", snap)
	awaitMoneyWorker(t, s)
	if calls.Load() != 1 {
		t.Fatal("refresh ignored one-minute cadence")
	}
	fail.Store(true)
	snap.Accounts[0].Money = m
	s.proRestock.moneyNext = time.Time{}
	s.refreshRestockMoney(restock.DefaultConfig(), p, "fixture", snap)
	awaitMoneyWorker(t, s)
	m = s.proRestock.money[1]
	if m.Error == "" || m.RateUSD != nil || len(m.Samples) > 0 {
		t.Fatal("failed read retained a fresh-looking previous rate")
	}
	fail.Store(false)
	snap.Accounts[0].Money = m
	s.proRestock.moneyNext = time.Time{}
	s.refreshRestockMoney(restock.DefaultConfig(), p, "fixture", snap)
	awaitMoneyWorker(t, s)
	m = s.proRestock.money[1]
	if m.Error != "" || m.RateUSD != nil || len(m.Samples) != 1 {
		t.Fatal("recovery must start a new contiguous sample window")
	}
}

func TestRestockMoneyWindowConfigAPI(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st}
	c := restock.DefaultConfig()
	raw, _ := json.Marshal(c)
	var payload map[string]any
	json.Unmarshal(raw, &payload)
	delete(payload, "money_window_minutes")
	call := func() *httptest.ResponseRecorder {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRecorder()
		s.saveProRestockConfig(r, httptest.NewRequest(http.MethodPut, "/api/pro-restock/config", bytes.NewReader(raw)))
		return r
	}
	if r := call(); r.Code != http.StatusOK {
		t.Fatal("legacy request rejected", r.Body.String())
	}
	got, err := st.ProRestockConfig()
	if err != nil || got.MoneyWindowMinutes != 3 {
		t.Fatal("API default", got, err)
	}
	payload["version"] = got.Version
	for _, invalid := range []any{0, -1, 61, 1.5, "five"} {
		payload["money_window_minutes"] = invalid
		if r := call(); r.Code != http.StatusBadRequest {
			t.Fatal("invalid minutes accepted", invalid, r.Body.String())
		}
	}
	payload["money_window_minutes"] = 7
	if r := call(); r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	got, err = st.ProRestockConfig()
	if err != nil || got.MoneyWindowMinutes != 7 || got.Enabled || got.TriggerMinutes != c.TriggerMinutes {
		t.Fatal("window did not save independently", got, err)
	}
	if r := call(); r.Code != http.StatusConflict {
		t.Fatal("stale version overwrote newer settings", r.Body.String())
	}
}

func TestRestockMoneyOldGroupResponseDiscarded(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture"}}`)
		case "/api/v1/admin/accounts/1/usage":
			close(started)
			<-release
			fmt.Fprintf(w, `{"code":0,"data":{"updated_at":%q}}`, time.Now().Format(time.RFC3339Nano))
		case "/api/v1/admin/usage/stats":
			fmt.Fprint(w, `{"code":0,"data":{"total_account_cost":40}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	p := model.ProSettings{Sub2: model.Sub2Settings{URL: upstream.URL, Email: "fixture@example.com"}}
	s := &Server{sub2: sub2.New()}
	s.proRestock.quotaBinding = restockBinding(p)
	snap := restock.Snapshot{Binding: restockBinding(p), Accounts: []restock.Account{{ID: 1, Ready: true}}}
	s.prepareRestockMoney("fixture", &snap)
	s.refreshRestockMoney(restock.DefaultConfig(), p, "fixture", snap)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("request did not start")
	}
	s.proRestock.mu.Lock()
	s.proRestock.moneyGeneration++ // Same server, new group/account scope.
	s.proRestock.money = map[int64]*restock.AccountMoney{}
	s.proRestock.mu.Unlock()
	close(release)
	awaitMoneyWorker(t, s)
	if len(s.proRestock.money) != 0 {
		t.Fatal("late response contaminated a different group")
	}
}
