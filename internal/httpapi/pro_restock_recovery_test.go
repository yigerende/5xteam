package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func TestProRestockCollectionRecovery(t *testing.T) {
	for _, manual := range []bool{false, true} {
		name := "scheduled"
		if manual {
			name = "manual"
		}
		t.Run(name, func(t *testing.T) {
			s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected supplier call") })
			configureProAutoFixture(t, s)
			s.proAutoCancel()
			c := restock.DefaultConfig()
			c.Enabled, c.Mode, c.GroupID = true, "auto", 1
			c.TimeEnabled, c.Confirmations, c.MinBatch, c.MaxBatch = false, 2, 1, 1
			c.CapacityPercent = 100 // Exercise soft pressure; real overload now has a separate fast path.
			c.CheckSeconds = 30
			c.FreshSeconds = 60
			c, err := st.SaveProRestockConfig(c)
			if err != nil {
				t.Fatal(err)
			}
			p, _, _, _ := st.ProSettings()
			pay, _, _ := st.GPTPaySettings()
			var launches atomic.Int32
			s.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
				launches.Add(1)
				return errors.New("stop test flow before payment")
			}}
			calls := 0
			var collectionErr error
			var finished time.Time
			s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
				calls++
				finished = time.Now()
				return restock.Snapshot{At: finished, Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity), Accounts: []restock.Account{{ID: 1, Ready: true, Capacity: 10, Used: 9}}}, collectionErr
			}
			tick := func(force bool) {
				t.Helper()
				if err := s.tickProRestock(context.Background(), force); err != nil {
					t.Fatal(err)
				}
			}
			// Begin accumulating pressure, then fail: old pressure must not survive.
			tick(true)
			if s.proRestock.confirmation != 1 {
				t.Fatal("missing first confirmation")
			}
			for _, failure := range []error{errors.New("monitor unavailable"), context.DeadlineExceeded} {
				collectionErr = failure
				tick(true)
				if s.proRestock.lastSnapshot.Valid || s.proRestock.decision.Suggested != 0 || s.proRestock.confirmation != 0 || launches.Load() != 0 {
					t.Fatal("failed collection did not pause new work and clear confirmations")
				}
				tasks, _ := st.ProRestockTasks()
				if len(tasks) != 0 {
					t.Fatal("failed collection created tasks")
				}
				if s.proRestock.next.Before(finished.Add(30 * time.Second)) {
					t.Fatal("retry interval not honored")
				}
			}
			before := calls
			tick(false)
			if calls != before {
				t.Fatal("retried before configured interval")
			}
			collectionErr = nil
			retry := func() {
				if !manual {
					s.proRestock.next = time.Now().Add(-time.Second)
				}
				tick(manual)
			}
			retry()
			if calls != before+1 || !s.proRestock.lastSnapshot.Valid || !s.proRestock.lastSnapshot.LoadValid || !s.proRestock.decision.DemandKnown || s.proRestock.confirmation != 1 || launches.Load() != 0 {
				t.Fatal("recovery must sample again and rebuild pressure confirmations")
			}
			retry()
			waitProAutoFinished(t, s, email)
			if launches.Load() != 1 || s.proRestock.decision.Started != 1 {
				t.Fatal("recovered demand did not resume existing Pro flow")
			}
			saved, _ := st.ProRestockConfig()
			if !reflect.DeepEqual(saved, c) {
				t.Fatal("collection failure or recovery changed configuration")
			}
		})
	}
}

func TestProRestockManualRetryBusyReturnsConflict(t *testing.T) {
	s, _, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected supplier call") })
	s.proAutoCancel()
	mux := http.NewServeMux()
	s.registerProRestock(mux)
	s.proRestock.tick.Lock()
	defer s.proRestock.tick.Unlock()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pro-restock/evaluate", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy retry returned old success: %d %s", rec.Code, rec.Body.String())
	}
}
