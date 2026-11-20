package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/sub2"
)

func TestProRestockCollectionRetainsSurvivingSIWCAccountMoney(t *testing.T) {
	s, _, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) {})
	s.proAutoCancel()
	s.proRestock.moneyBusy = true
	s.proRestock.quotaNext = time.Now().Add(time.Hour)
	ids := []int{1, 2}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case "/api/v1/auth/login":
			data = map[string]any{"access_token": "fixture"}
		case "/api/v1/admin/groups/all":
			data = []any{map[string]any{"id": 1, "name": "Pro", "platform": "openai"}}
		case "/api/v1/admin/accounts":
			items := []any{}
			for _, id := range ids {
				items = append(items, map[string]any{"id": id, "platform": "openai", "type": "oauth", "status": "active", "schedulable": true, "concurrency": 60, "group_ids": []int{1}, "credentials": map[string]any{"plan_type": "pro"}})
			}
			data = map[string]any{"items": items, "total": len(items)}
		case "/api/v1/admin/ops/concurrency":
			loads := map[string]any{}
			for _, id := range ids {
				loads[fmt.Sprint(id)] = map[string]any{"account_id": id, "current_in_use": 20, "waiting_in_queue": 0, "max_capacity": 60}
			}
			data = map[string]any{"enabled": true, "timestamp": time.Now(), "account": loads}
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	defer upstream.Close()
	c := restock.DefaultConfig()
	c.GroupID = 1
	p := model.ProSettings{Provider: "sub2", Sub2: model.Sub2Settings{URL: upstream.URL, Email: "fixture@example.com", GroupIDs: []int64{1}, AuthMode: "siwc", AccountConcurrency: 60}}
	s.proRestock.quotaBinding = restockBinding(p)
	if _, err := s.collectProRestock(context.Background(), c, p, "fixture"); err != nil {
		t.Fatal(err)
	}
	now, rate := time.Now(), 2.
	history := &restock.AccountMoney{At: now, Day: now.UTC().Format("2006-01-02"), RateUSD: &rate, Windows: []restock.MoneyWindow{{Seconds: 604800, ResetAt: now.Add(time.Hour), UsedPercent: 50, UsedUSD: 100}}, Samples: []restock.MoneySample{{At: now.Add(-3 * time.Minute), Cost: 10}, {At: now, Cost: 16}}}
	s.proRestock.money[2] = history
	ids = []int{2}
	snap, err := s.collectProRestock(context.Background(), c, p, "fixture")
	if err != nil || len(snap.Accounts) != 1 || snap.Accounts[0].Money != history || !snap.Accounts[0].SIWC {
		t.Fatalf("survivor lost history in real collection: %+v %v", snap, err)
	}
	forecast := restock.ForecastMoney(c, snap, time.Now())
	if forecast.State != "ready" || forecast.RateUSD == nil || *forecast.RateUSD != rate {
		t.Fatalf("remaining account fell back to fresh sampling: %+v", forecast)
	}
}

func TestRestockMoneyRosterChangesKeepOtherAccountHistory(t *testing.T) {
	s := &Server{}
	now := time.Now()
	first := restock.Account{ID: 1, Plan: "pro", Ready: true}
	second := restock.Account{ID: -2, RemoteID: 2, SourceURL: "https://siwc.example", TargetID: "siwc", SIWC: true, Plan: "pro", Ready: true}
	snap := restock.Snapshot{Accounts: []restock.Account{first, second}}
	s.prepareRestockMoney("pool", &snap)
	history := &restock.AccountMoney{At: now, Day: now.UTC().Format("2006-01-02"), Samples: []restock.MoneySample{{At: now.Add(-3 * time.Minute), Cost: 10}, {At: now, Cost: 40}}}
	s.proRestock.money[second.ID] = history
	epoch := snap.Accounts[1].MoneyEpoch
	next := now.Add(time.Minute)
	s.proRestock.moneyNext = next
	for _, members := range [][]restock.Account{{second}, {first, second}, {{ID: 1, Plan: "pro", Ready: false}, second}, {{ID: 1, Plan: "plus", Ready: true}, second}} {
		snap.Accounts = members
		s.prepareRestockMoney("pool", &snap)
		if s.proRestock.money[second.ID] != history || s.proRestock.moneyAccounts[second.ID].epoch != epoch || !s.proRestock.moneyNext.Equal(next) {
			t.Fatal("another account's departure/readiness/plan reset the stable account or sampling schedule")
		}
	}
	second.Plan = "plus"
	snap.Accounts = []restock.Account{second}
	s.prepareRestockMoney("pool", &snap)
	if snap.Accounts[0].Money != nil || snap.Accounts[0].MoneyEpoch == epoch {
		t.Fatal("the changed account reused a different plan's samples")
	}
	s.proRestock.money[second.ID] = history
	s.prepareRestockMoney("another-pool", &snap)
	if snap.Accounts[0].Money != nil {
		t.Fatal("target/group changes must invalidate old-scope samples")
	}
}

func TestRestockMoneyLateReadChecksIndividualAccountEpoch(t *testing.T) {
	for _, change := range []string{"other_left", "removed", "rejoined", "plan", "unready"} {
		t.Run(change, func(t *testing.T) {
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
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer upstream.Close()
			p := model.ProSettings{Sub2: model.Sub2Settings{URL: upstream.URL, Email: "fixture@example.com"}}
			s := &Server{sub2: sub2.New()}
			s.proRestock.quotaBinding = restockBinding(p)
			a := restock.Account{ID: 1, Plan: "pro", Ready: true}
			snap := restock.Snapshot{Binding: restockBinding(p), Accounts: []restock.Account{a, {ID: 2}}}
			s.prepareRestockMoney("pool", &snap)
			s.refreshRestockMoney(restock.DefaultConfig(), p, "fixture", snap)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("request did not start")
			}
			snap.Accounts = []restock.Account{a}
			if change == "removed" || change == "rejoined" {
				snap.Accounts = nil
			} else if change == "plan" {
				snap.Accounts[0].Plan = "plus"
			} else if change == "unready" {
				snap.Accounts[0].Ready = false
			}
			s.prepareRestockMoney("pool", &snap)
			if change == "rejoined" {
				snap.Accounts = []restock.Account{a}
				s.prepareRestockMoney("pool", &snap)
			}
			close(release)
			awaitMoneyWorker(t, s)
			if (s.proRestock.money[1] != nil) != (change == "other_left") {
				t.Fatal("late response accepted for changed account or rejected for unchanged account")
			}
		})
	}
}
