package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

func TestProRestockCountsServingAccountsAcrossModelChanges(t *testing.T) {
	s, _, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) {})
	s.proAutoCancel()
	s.proRestock.quotaBusy = true
	s.proRestock.moneyBusy = true
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case "/api/v1/auth/login":
			data = map[string]any{"access_token": "test"}
		case "/api/v1/admin/groups/all":
			data = []any{map[string]any{"id": 6, "name": "Pro", "platform": "openai"}}
		case "/api/v1/admin/accounts":
			if r.URL.Query().Get("group") != "6" {
				t.Error("wrong monitoring group")
			}
			items := []any{}
			for _, id := range []int{137, 138} {
				models := map[string]string{"gpt-6.1-sol": "gpt-6.1-sol"}
				if id == 138 {
					models["gpt-6.2-sol"] = "gpt-6.2-sol"
				}
				items = append(items, map[string]any{"id": id, "platform": "openai", "type": "oauth", "status": "active", "schedulable": true, "concurrency": 60, "group_ids": []int{6}, "credentials": map[string]any{"model_mapping": models}})
			}
			data = map[string]any{"total": 2, "items": items}
		case "/api/v1/admin/ops/concurrency":
			data = map[string]any{"enabled": true, "timestamp": time.Now(), "account": map[string]any{
				"137": map[string]any{"account_id": 137, "current_in_use": 32, "waiting_in_queue": 0, "max_capacity": 60},
				"138": map[string]any{"account_id": 138, "current_in_use": 29, "waiting_in_queue": 0, "max_capacity": 60},
			}}
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	defer remote.Close()
	c := restock.DefaultConfig()
	c.GroupID, c.Reserve, c.CapacityPercent = 6, 2, 100
	c.TimeEnabled = false
	p := model.ProSettings{Provider: "sub2", Sub2: model.Sub2Settings{URL: remote.URL, Email: "admin@test.example", GroupIDs: []int64{6}, AccountConcurrency: 60}}
	for _, auth := range []string{"", "siwc"} {
		p.Sub2.AuthMode = auth
		for _, models := range [][]string{{"gpt-6.1-sol"}, {"gpt-6.1-sol", "gpt-6.2-sol"}} {
			p.Sub2.Models = models
			snap, err := s.collectProRestock(context.Background(), c, p, "test")
			if err != nil || !snap.Valid || !snap.LoadValid {
				t.Fatalf("monitor: %v %+v", err, snap)
			}
			d := restock.Evaluate(c, snap, nil, restock.Limits{}, c.Confirmations, 60, 5, time.Now())
			if d.Usable != 2 || d.ReportedCapacity != 120 || d.Capacity != 120 || d.Used != 61 || d.ReserveNeed != 0 || d.ConcurrencyNeed != 0 || d.Suggested != 0 {
				t.Fatalf("auth=%q models=%v: serving account lost or spurious restock: %+v", auth, models, d)
			}
		}
		// Fresh exhausted quota still removes supply despite the model-independent count.
		s.proRestock.quota[137] = []restock.Window{{Seconds: 604800, ResetAt: time.Now().Add(time.Hour).Unix(), Used: 100, At: time.Now()}}
		snap, err := s.collectProRestock(context.Background(), c, p, "test")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range snap.Accounts {
			if a.ID == 137 && a.Ready {
				t.Fatal("exhausted quota counted as available")
			}
		}
		delete(s.proRestock.quota, 137)
	}
}
