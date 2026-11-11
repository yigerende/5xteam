package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProRestockMonitorQuality(t *testing.T) {
	s, _, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) {})
	s.proAutoCancel()
	s.proRestock.quotaNext = time.Now().Add(time.Hour)
	s.proRestock.moneyBusy = true // This test isolates account/load collection.
	mode := "zero"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case "/api/v1/auth/login":
			data = map[string]any{"access_token": "test-token"}
		case "/api/v1/admin/groups/all":
			data = []any{map[string]any{"id": 1, "name": "Pro", "platform": "openai"}}
		case "/api/v1/admin/accounts":
			if r.URL.Query().Get("group") != "1" {
				t.Error("must query only monitored group")
			}
			account := map[string]any{"id": 1, "platform": "openai", "type": "oauth", "status": "active", "schedulable": true, "concurrency": 10, "group_ids": []int{1}}
			if mode == "native_apikey" {
				account["type"] = "apikey"
			}
			if mode == "native_unknown_type" {
				delete(account, "type")
			}
			if mode == "native_shared" {
				account["group_ids"] = []int{1, 2}
			}
			if mode == "foreign_member" {
				account["group_ids"] = []int{2}
			}
			items := []any{account}
			total := 1
			if mode == "native_empty" {
				items, total = []any{}, 0
			}
			if mode == "duplicate" {
				items = append(items, account)
				total = 2
			}
			if mode == "upstream_fault" {
				account["status"] = "error"
			}
			data = map[string]any{"items": items, "total": total}
		case "/api/v1/admin/ops/concurrency":
			if r.URL.Query().Get("group_id") != "1" {
				t.Error("must query only monitored group load")
			}
			if r.URL.Query().Has("strict") {
				t.Error("native monitoring must not require strict extension")
			}
			if mode == "http_error" {
				http.Error(w, "monitor temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			at := time.Now()
			if mode == "stale" {
				at = at.Add(-time.Hour)
			}
			load := map[string]any{"account_id": 1, "current_in_use": 0, "waiting_in_queue": 0, "max_capacity": 10, "load_known": true}
			if mode == "no_known" || strings.HasPrefix(mode, "native_") {
				delete(load, "load_known")
			}
			if mode == "unknown" {
				load["load_known"] = false
			}
			if mode == "native_pressure" {
				load["current_in_use"] = 9
			}
			if mode == "capacity_changed" {
				load["max_capacity"] = 20
			}
			quality := map[string]any{"complete": true, "expected": 1, "succeeded": 1, "failed": 0}
			payload := map[string]any{"enabled": true, "timestamp": at, "quality": quality, "account": map[string]any{"1": load}}
			if mode == "no_quality" || strings.HasPrefix(mode, "native_") {
				delete(payload, "quality")
			}
			if mode == "incomplete_quality" {
				quality["complete"], quality["succeeded"], quality["failed"] = false, 0, 1
			}
			if mode == "missing_load" || mode == "native_empty" {
				payload["account"] = map[string]any{}
			}
			data = payload
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	defer server.Close()
	c := restock.DefaultConfig()
	c.GroupID = 1
	c.Reserve = 1
	p := model.ProSettings{Provider: "sub2", Sub2: model.Sub2Settings{URL: server.URL, Email: "admin@test.com", GroupIDs: []int64{1, 2}, AccountConcurrency: 10}}
	s.proRestock.quotaBinding = restockBinding(p)
	for _, name := range []string{"zero", "no_quality", "no_known", "native_zero", "native_shared", "native_empty", "native_pressure", "native_apikey", "native_unknown_type", "unknown", "incomplete_quality", "http_error", "missing_load", "foreign_member", "duplicate", "stale", "capacity_changed", "upstream_fault"} {
		t.Run(name, func(t *testing.T) {
			mode = name
			snap, err := s.collectProRestock(context.Background(), c, p, "test-secret")
			if err != nil {
				snap.Valid = false
			}
			d := restock.Evaluate(c, snap, nil, restock.Limits{}, 2, 10, 5, time.Now())
			if name == "native_apikey" || name == "native_unknown_type" || name == "native_zero" {
				policy := c
				policy.EmptyGroupCount = 1
				policy.Reserve = 0
				policy.ConcurrencyEnabled = false
				policy.TimeEnabled = false
				empty := restock.Evaluate(policy, snap, nil, restock.Limits{}, 2, 10, 5, time.Now())
				want := 0
				if name == "native_apikey" {
					want = 1
				}
				if empty.EmptyGroupNeed != want || empty.Suggested != want {
					t.Fatalf("OAuth empty-group: %s %+v", name, empty)
				}
				if name == "native_unknown_type" && (snap.MonitorOAuthCount != nil || snap.MonitorOAuthError == "") {
					t.Fatal("unknown OAuth count considered complete")
				}
			}
			if name == "zero" || name == "no_quality" || name == "no_known" || strings.HasPrefix(name, "native_") {
				if err != nil || !snap.Valid || !snap.LoadValid || !d.DemandKnown {
					t.Fatalf("valid monitoring data rejected: %+v %v", snap, err)
				}
				if name == "native_shared" && (d.Usable != 1 || d.ReportedCapacity != 10 || !snap.Accounts[0].Shared) {
					t.Fatalf("multi-group account dropped: %+v", d)
				}
				if name == "native_pressure" {
					if d.Used != 9 || d.Suggested == 0 {
						t.Fatalf("native concurrency pressure ignored: %+v", d)
					}
				} else if d.Used != 0 {
					t.Fatalf("legitimate zero misread: %+v", d)
				}
				if name == "native_empty" {
					emptyConfig := c
					emptyConfig.Reserve = 0
					empty := restock.Evaluate(emptyConfig, snap, nil, restock.Limits{}, 2, 10, 5, time.Now())
					if len(snap.Accounts) != 0 || empty.Suggested != 0 {
						t.Fatalf("empty native group creates demand without reserve: %+v", empty)
					}
				}
			} else if d.DemandKnown || d.Suggested != 0 {
				t.Fatalf("unknown or faulty data used as demand: %+v", d)
			}
		})
	}
	mode = "zero"
	for _, age := range []time.Duration{time.Minute, time.Hour, -time.Minute} {
		s.proRestock.quota = map[int64][]restock.Window{1: {{Seconds: 3600, ResetAt: time.Now().Add(time.Hour).Unix(), Used: 100, At: time.Now().Add(-age)}}}
		snap, err := s.collectProRestock(context.Background(), c, p, "test-secret")
		if err != nil || len(snap.Accounts) != 1 {
			t.Fatalf("quota monitor: %v", err)
		}
		wantReady := age != time.Minute
		if snap.Accounts[0].Ready != wantReady {
			t.Errorf("quota age %v: ready=%v want=%v", age, snap.Accounts[0].Ready, wantReady)
		}
	}
}

func TestProRestockStageETAIsAnchored(t *testing.T) {
	now := time.Now()
	started := now.Add(-4 * time.Minute)
	oauth := now.Add(-time.Minute)
	task := restock.Task{Binding: "pool", Plan: "pro", SubmittedAt: &started, Stages: map[string]time.Time{"oauth:running": oauth}}
	ready := now.Add(2 * time.Minute)
	historical := restock.Task{Binding: "pool", Plan: "pro", ReadyAt: &ready, Stages: map[string]time.Time{"oauth:running": now}}
	history := []restock.Task{historical, historical}
	if got := restockRemainingLead(task, history, 2, 10, now); got != 1 {
		t.Fatalf("stage estimate %v", got)
	}
	if got := restockRemainingLead(task, history, 2, 10, now.Add(2*time.Minute)); got >= 0 {
		t.Fatal("stalled ETA drifted")
	}
	historical.Plan = "other"
	if got := restockRemainingLead(task, []restock.Task{historical}, 2, 10, now); got != 6 {
		t.Fatal("untrusted history did not fall back")
	}
}
