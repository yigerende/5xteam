package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/restock"
)

func TestRestockLargeHistoryPageSaveAndCalculation(t *testing.T) {
	dir := t.TempDir()
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected supplier call") }, dir)
	s.proAutoCancel()
	s.proAutoWG.Wait()
	configureProAutoFixture(t, s)
	var polls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case "/api/v1/auth/login":
			data = map[string]any{"access_token": "fixture"}
		case "/api/v1/admin/groups/all":
			data = []any{map[string]any{"id": 6, "name": "fixture", "platform": "openai"}}
		case "/api/v1/admin/accounts":
			items := []any{}
			for i := 1; i <= 4; i++ {
				items = append(items, map[string]any{"id": i, "platform": "openai", "type": "oauth", "status": "active", "schedulable": true, "concurrency": 60, "group_ids": []int{6}})
			}
			data = map[string]any{"items": items, "total": 4}
		case "/api/v1/admin/ops/concurrency":
			polls.Add(1)
			accounts := map[string]any{}
			for i := 1; i <= 4; i++ {
				accounts[fmt.Sprint(i)] = map[string]any{"account_id": i, "current_in_use": 15, "waiting_in_queue": 0, "max_capacity": 60, "load_known": true}
			}
			data = map[string]any{"enabled": true, "timestamp": time.Now(), "quality": map[string]any{"complete": true, "expected": 4, "succeeded": 4, "failed": 0}, "account": accounts}
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	defer upstream.Close()
	p, secret, _, err := st.ProSettings()
	if err != nil {
		t.Fatal(err)
	}
	p.Sub2.URL, p.Sub2.GroupIDs, p.Sub2.AccountConcurrency = upstream.URL, []int64{6}, 60
	if _, err = st.SaveProSettings(p, secret, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../restock/testdata/production_20261121.json")
	if err != nil {
		t.Fatal(err)
	}
	var c restock.Config
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	c.Version = restock.DefaultConfig().Version
	if c, err = st.SaveProRestockConfig(c); err != nil {
		t.Fatal(err)
	}
	s.proRestock.quotaBinding = restockBinding(p)
	s.proRestock.quotaBusy, s.proRestock.moneyBusy = true, true
	// Exercise real HTTP handlers and tick reconciliation; only external Sub2
	// requests are replaced with deterministic local responses.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "state.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	taskStmt, err := tx.Prepare(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	decisionStmt, err := tx.Prepare(`INSERT INTO pro_restock_decisions VALUES(?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	started := time.Now()
	for i := 0; i < 50000; i++ {
		at := now.Add(-time.Duration(72*3600+i) * time.Second)
		if i < 40 {
			at = now.Add(-time.Duration(100+i) * time.Second)
		}
		task := restock.Task{ID: fmt.Sprintf("history-%05d", i), DecisionID: fmt.Sprintf("decision-%05d", i), Email: fmt.Sprintf("history-%d@example.invalid", i), Status: "ready", CreatedAt: at, UpdatedAt: at, ReadyAt: &at, Binding: restockBinding(p), GroupID: 6, AccountID: int64(1000 + i), Plan: "fixture", Capacity: 60}
		payload, _ := json.Marshal(task)
		if _, err = taskStmt.Exec(task.ID, task.Email, task.Status, at.UTC().Format(time.RFC3339Nano), 0, string(payload)); err != nil {
			t.Fatal(err)
		}
		d := restock.Decision{ID: task.DecisionID, At: at, Config: c, Version: c.Version, Snapshot: restock.Snapshot{ID: "historical", At: at, Binding: task.Binding, GroupID: 6, Valid: true, LoadValid: true}, Reason: "historical fixture"}
		for j := 0; j < 4; j++ {
			d.Snapshot.Accounts = append(d.Snapshot.Accounts, restock.Account{ID: int64(j + 1), Ready: true, Capacity: 60, Used: 15})
		}
		payload, _ = json.Marshal(d)
		if _, err = decisionStmt.Exec(d.ID, at.UTC().Format(time.RFC3339Nano), string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	taskStmt.Close()
	decisionStmt.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Logf("seed: 50,000 tasks + 50,000 decisions in %s", time.Since(started))
	tasks, err := st.ProRestockTasks()
	if err != nil || len(tasks) != 40 {
		t.Fatalf("history leaked into hot task list: %d %v", len(tasks), err)
	}
	mux := http.NewServeMux()
	s.registerProRestock(mux)
	measure := func(name string, fn func()) {
		t.Helper()
		start := time.Now()
		fn()
		elapsed := time.Since(start)
		t.Logf("%s: %s", name, elapsed)
		if elapsed >= 5*time.Second {
			t.Errorf("%s exceeded a five-second check period: %s", name, elapsed)
		}
	}
	read := func(url string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != 200 {
			t.Errorf("%s returned %d: %.200s", url, rec.Code, rec.Body.String())
		}
		if rec.Body.Len() > 1<<20 {
			t.Errorf("%s returned historical bulk payload: %d", url, rec.Body.Len())
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Error(err)
			return
		}
		switch {
		case strings.Contains(url, "records?page="):
			var page struct {
				Total int                `json:"total"`
				Items []restock.Decision `json:"items"`
			}
			if err := json.Unmarshal(envelope.Data, &page); err != nil || page.Total < 50000 || len(page.Items) != 10 {
				t.Errorf("history pagination lost rows: total=%d items=%d error=%v", page.Total, len(page.Items), err)
			}
		case strings.HasSuffix(url, "/tasks"):
			var tasks []restock.Task
			if err := json.Unmarshal(envelope.Data, &tasks); err != nil || len(tasks) != 1 || tasks[0].ID != "history-49999" {
				t.Errorf("archived detail missing: %v %v", tasks, err)
			}
		case url == "/api/pro-restock":
			var page struct {
				Tasks []restock.Task `json:"tasks"`
			}
			if err := json.Unmarshal(envelope.Data, &page); err != nil || len(page.Tasks) != 40 {
				t.Errorf("live page loaded archived rows: count=%d error=%v", len(page.Tasks), err)
			}
		}
	}
	measure("page first load", func() { read("/api/pro-restock") })
	for _, page := range []int{1, 2500, 5000} {
		measure(fmt.Sprintf("records page %d", page), func() { read(fmt.Sprintf("/api/pro-restock/records?page=%d&page_size=10", page)) })
	}
	measure("historical task details", func() { read("/api/pro-restock/records/decision-49999/tasks") })
	var timings []time.Duration
	for i := 0; i < 12; i++ {
		start := time.Now()
		if err = s.tickProRestock(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		timings = append(timings, time.Since(start))
		if s.proRestock.decision.Started != 0 || s.proRestock.decision.Usable != 4 {
			t.Fatal("historical tasks changed live supply", s.proRestock.decision)
		}
		before := polls.Load()
		if err = s.tickProRestock(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if polls.Load() != before {
			t.Fatal("polled before the five-second cadence was due")
		}
	}
	sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
	t.Logf("12 complete monitor/calculation rounds: median=%s p95/max=%s", timings[6], timings[11])
	if timings[11] >= 5*time.Second {
		t.Fatal("history made tick exceed check period")
	}
	measure("save enabled production config", func() {
		rec := payCall(t, s.saveProRestockConfig, "", "", c)
		if rec.Code != 200 {
			t.Fatalf("config save: %d %.300s", rec.Code, rec.Body.String())
		}
	})
	c, err = st.ProRestockConfig()
	if err != nil || c.CheckSeconds != 5 {
		t.Fatal("save changed cadence", err)
	}
	measure("16 concurrent page loads plus config save", func() {
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); read("/api/pro-restock") }()
		}
		rec := payCall(t, s.saveProRestockConfig, "", "", c)
		wg.Wait()
		if rec.Code != 200 {
			t.Fatalf("concurrent save: %d %.200s", rec.Code, rec.Body.String())
		}
	})
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM pro_restock_tasks`).Scan(&count); err != nil || count != 50000 {
		t.Fatal("test unexpectedly created or removed tasks", count, err)
	}
}
