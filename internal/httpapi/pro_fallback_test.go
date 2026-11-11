package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fallbackMock struct {
	before           func(*http.Request)
	mu               sync.Mutex
	on               map[int64]bool
	group            map[int64]int64
	memberships      map[int64][]int64
	failGroup        int64
	writes           map[int64]int
	fail             map[int64]bool
	ambiguous        map[int64]bool
	logins           int
	reads            int
	sourceCount      int
	sourceType       string
	sourceFailure    bool
	sourceMissing    bool
	sourceIncomplete bool
	sourceCounts     map[int64]int
	sourceErrors     map[int64]bool
	server           *httptest.Server
}

func newFallbackMock(t *testing.T) *fallbackMock {
	m := &fallbackMock{on: map[int64]bool{1: false, 2: false, 3: true}, group: map[int64]int64{1: 7, 2: 7, 3: 7}, writes: map[int64]int{}, fail: map[int64]bool{}, ambiguous: map[int64]bool{}}
	m.memberships = map[int64][]int64{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.before != nil {
			m.before(r)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		write := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": v}) }
		account := func(id int64) map[string]any {
			kind := "oauth"
			if id == 2 {
				kind = "apikey"
			}
			ids := m.memberships[id]
			if ids == nil {
				ids = []int64{m.group[id]}
			}
			return map[string]any{"id": id, "name": fmt.Sprintf("fallback-%d", id), "platform": "openai", "type": kind, "status": "active", "schedulable": m.on[id], "group_ids": ids, "credentials": map[string]string{"api_key": "DO-NOT-EXPOSE-KEY", "refresh_token": "DO-NOT-EXPOSE-TOKEN"}}
		}
		switch r.URL.Path {
		case "/api/v1/auth/login":
			m.logins++
			write(map[string]string{"access_token": "fake-admin-token"})
			return
		case "/api/v1/admin/groups/all":
			groups := []map[string]any{{"id": 7, "name": "fallback", "platform": "openai"}, {"id": 8, "name": "fallback-two", "platform": "openai"}}
			if !m.sourceMissing {
				groups = append(groups, map[string]any{"id": 9, "name": "main", "platform": "openai"})
			}
			for id := range m.sourceCounts {
				if id != 9 {
					groups = append(groups, map[string]any{"id": id, "name": fmt.Sprintf("monitor-%d", id), "platform": "openai"})
				}
			}
			write(groups)
			return
		case "/api/v1/admin/accounts":
			sourceID, _ := strconv.ParseInt(r.URL.Query().Get("group"), 10, 64)
			sourceCount, extraSource := m.sourceCounts[sourceID]
			if sourceID == 9 {
				sourceCount = m.sourceCount
			}
			if sourceID == 9 || extraSource {
				if (sourceID == 9 && m.sourceFailure) || m.sourceErrors[sourceID] {
					w.WriteHeader(503)
					return
				}
				items := []any{}
				if !m.sourceIncomplete {
					for i := 0; i < sourceCount; i++ {
						kind := m.sourceType
						if kind == "" {
							kind = "oauth"
						}
						items = append(items, map[string]any{"id": 100 + i, "platform": "openai", "type": kind, "group_ids": []int64{sourceID}})
					}
				}
				write(map[string]any{"items": items, "total": sourceCount})
				return
			}

			group, _ := strconv.ParseInt(r.URL.Query().Get("group"), 10, 64)
			if (group != 7 && group != 8) || r.URL.Query().Get("platform") != "openai" {
				t.Error("wrong account filters")
			}
			if group == m.failGroup {
				w.WriteHeader(503)
				return
			}
			items := []any{}
			for _, id := range []int64{1, 2, 3} {
				a := account(id)
				for _, g := range a["group_ids"].([]int64) {
					if g == group {
						items = append(items, a)
						break
					}
				}
			}
			write(map[string]any{"items": items, "total": len(items)})
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"), "/")
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		if _, ok := m.on[id]; !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			m.reads++
			write(account(id))
			return
		}
		if r.Method != http.MethodPost || len(parts) != 2 || parts[1] != "schedulable" {
			t.Error("unexpected mutation", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 1 {
			t.Error("mutation changed fields beyond schedulable", body)
		}
		m.writes[id]++
		if m.fail[id] {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"message":"DO-NOT-EXPOSE-KEY"}`))
			return
		}
		m.on[id] = body["schedulable"]
		if m.ambiguous[id] {
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"message":"lost response DO-NOT-EXPOSE-TOKEN"}`))
			return
		}
		write(account(id))
	}))
	t.Cleanup(m.server.Close)
	return m
}

func TestFallbackCountsOAuthInsteadOfAPIKeysOrLocalTasks(t *testing.T) {
	s, _, m, _ := fallbackFixture(t)
	m.sourceCount = 2
	m.sourceType = "apikey"
	if err := s.tickProFallback(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !m.on[1] || !m.on[2] || s.proFallback.observation.Count == nil || *s.proFallback.observation.Count != 0 {
		t.Fatal("API key prevented fallback")
	}
	m.sourceType = "unrecognized"
	_ = s.tickProFallback(context.Background(), true)
	if !m.on[1] || !m.on[2] || s.proFallback.observation.Error == "" {
		t.Fatal("unknown account type changed scheduling")
	}
	m.sourceType = "oauth"
	if err := s.tickProFallback(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if m.on[1] || m.on[2] || *s.proFallback.observation.Count != 2 {
		t.Fatal("unmanaged downstream OAuth did not stop fallback")
	}
}
func fallbackFixture(t *testing.T) (*Server, *store.Store, *fallbackMock, *time.Time) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := newFallbackMock(t)
	p := model.DefaultProSettings()
	p.Sub2.URL, p.Sub2.Email, p.Sub2.GroupIDs = m.server.URL, "main@example.test", []int64{9}
	if _, err = st.SaveProSettings(p, "main-secret", ""); err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-10-16T05:00:00+08:00")
	s := &Server{store: st}
	s.proFallback.now = func() time.Time { return now }
	c := restock.DefaultConfig()
	c.Enabled = true
	c.Mode = "auto"
	c.GroupID = 9
	c.NoRestockEnabled = true
	c.NoRestockStart = "05:00"
	c.NoRestockEnd = "09:00"
	if _, err = st.SaveProRestockConfig(c); err != nil {
		t.Fatal(err)
	}
	id, err := st.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-fallback", Provider: "sub2", Sub2: model.Sub2Settings{URL: m.server.URL, Email: "test@example.test"}, Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	f := restock.DefaultFallbackConfig()
	f.Enabled = true
	f.URL = m.server.URL
	f.Email = "test@example.test"
	f.GroupID = 7
	f.AccountIDs = []int64{1, 2}
	if _, err = st.SaveProFallbackConfig(f, id); err != nil {
		t.Fatal(err)
	}
	return s, st, m, &now
}
func TestFallbackCountSwitchRestartAndIdempotency(t *testing.T) {
	s, st, m, now := fallbackFixture(t)
	ctx := context.Background()
	if err := s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !m.on[1] || !m.on[2] || m.writes[3] != 0 {
		t.Fatal("selected accounts only", m.on, m.writes)
	}
	if err := s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	if m.writes[1] != 1 || m.writes[2] != 1 || m.logins != 2 {
		t.Fatal("not idempotent or login cache missed", m.writes, m.logins)
	}
	*now = now.Add(4*time.Hour + 5*time.Minute)
	restarted := &Server{store: st}
	restarted.proFallback.now = func() time.Time { return *now }
	if err := restarted.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !m.on[1] || m.writes[1] != 1 {
		t.Fatal("empty group should remain protected after restart and outside daily pause")
	}
	*now = now.Add(5 * time.Minute)
	m.sourceCount = 1
	if err := restarted.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	if m.on[1] || m.on[2] || !m.on[3] || m.writes[1] != 2 {
		t.Fatal("nonempty group must turn off fallback", m.on, m.writes)
	}
	// Manual changes are reconciled outside the interval too.
	m.on[1] = true
	if err := restarted.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	if m.on[1] {
		t.Fatal("state drift ignored")
	}
}
func TestFallbackPartialFailureRetryAndAmbiguousResult(t *testing.T) {
	s, st, m, now := fallbackFixture(t)
	ctx := context.Background()
	m.fail[1] = true
	m.ambiguous[2] = true
	if err := s.tickProFallback(ctx, true); err == nil {
		t.Fatal("failure hidden")
	}
	if m.on[1] || !m.on[2] {
		t.Fatal("partial execution failed", m.on)
	}
	bindings, _ := st.ProFallbackBindings()
	for _, b := range bindings {
		if b.Observed != nil || b.Error == "" || strings.Contains(b.Error, "DO-NOT") {
			t.Fatal("unsafe failure result", b)
		}
	}
	reads := m.reads
	if err := s.tickProFallback(ctx, false); err != nil {
		t.Fatal(err)
	}
	if m.reads != reads {
		t.Fatal("retry interval bypassed")
	}
	m.fail[1] = false
	m.ambiguous[2] = false
	*now = now.Add(10 * time.Second)
	if err := s.tickProFallback(ctx, false); err != nil {
		t.Fatal(err)
	}
	if !m.on[1] || !m.on[2] || m.writes[1] != 2 || m.writes[2] != 1 {
		t.Fatal("ambiguous write repeated or failure not retried", m.writes)
	}
}
func TestFallbackIndependentOfRestockDeselectDisableAndGroupChange(t *testing.T) {
	s, st, m, _ := fallbackFixture(t)
	ctx := context.Background()
	c, _ := st.ProRestockConfig()
	c.Mode = "observe"
	c.Enabled = false
	c.NoRestockEnabled = false
	c, _ = st.SaveProRestockConfig(c)
	if err := s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !m.on[1] || !m.on[2] {
		t.Fatal("restock pause or observe disabled fallback")
	}
	c.Mode = "auto"
	c, _ = st.SaveProRestockConfig(c)
	if err := s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	m.group[1] = 8
	if err := s.tickProFallback(ctx, true); err == nil {
		t.Fatal("moved account should be reported")
	}
	if m.on[1] || !m.on[2] {
		t.Fatal("moved account still on")
	}
	f, id, _ := st.ProFallbackConfig()
	f.AccountIDs = []int64{2}
	f, _ = st.SaveProFallbackConfig(f, id)
	if err := s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	b, _ := st.ProFallbackBindings()
	if len(b) != 1 || b[0].AccountID != 2 {
		t.Fatal("deselected binding not drained", b)
	}
	f.Enabled = false
	if _, err := st.SaveProFallbackConfig(f, id); err != nil {
		t.Fatal(err)
	}
	if err := s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	b, _ = st.ProFallbackBindings()
	if len(b) != 0 || m.on[2] || !m.on[3] {
		t.Fatal("disabled cleanup", b, m.on)
	}
}
func TestFallbackConnectionChangeDrainsPinnedAccounts(t *testing.T) {
	s, st, old, _ := fallbackFixture(t)
	ctx := context.Background()
	if err := s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	next := newFallbackMock(t)
	f, _, _ := st.ProFallbackConfig()
	f.URL = next.server.URL
	f.AccountIDs = []int64{2}
	id, err := st.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-fallback", Provider: "sub2", Sub2: model.Sub2Settings{URL: next.server.URL, Email: f.Email}, Secret: "new-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveProFallbackConfig(f, id); err != nil {
		t.Fatal(err)
	}
	if err = s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	if old.on[1] || old.on[2] || !next.on[2] || next.on[1] {
		t.Fatal("connection cleanup wrong")
	}
	b, _ := st.ProFallbackBindings()
	if len(b) != 1 || b[0].ConnectionID != id {
		t.Fatal(b)
	}
}
func TestFallbackAPIReadOnlyPreviewSecretsAndValidation(t *testing.T) {
	s, st, m, _ := fallbackFixture(t)
	mux := http.NewServeMux()
	s.registerProFallback(mux)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "DO-NOT") || strings.Contains(w.Body.String(), "fake-admin-token") {
			t.Fatal("upstream secret exposed")
		}
		return w
	}
	f, _, _ := st.ProFallbackConfig()
	v := fallbackInput{FallbackConfig: f}
	for _, path := range []string{"groups", "accounts"} {
		w := call("POST", "/api/pro-restock/fallback/"+path, v)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if len(m.writes) != 0 {
		t.Fatal("preview mutated scheduling")
	}
	v.AccountIDs = []int64{99}
	if w := call("PUT", "/api/pro-restock/fallback", v); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	v.AccountIDs = []int64{1, 2}
	v.URL = "https://changed.example.test"
	if w := call("PUT", "/api/pro-restock/fallback", v); w.Code != 400 {
		t.Fatal("credential reuse on changed endpoint", w.Code)
	}
	v.URL = f.URL
	if w := call("PUT", "/api/pro-restock/fallback", v); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", "/api/pro-restock/fallback", v); w.Code != 409 {
		t.Fatal("version conflict not detected", w.Code)
	}
	if w := call("GET", "/api/pro-restock/fallback", nil); strings.Contains(w.Body.String(), `"password"`) {
		t.Fatal("password field leaked")
	}
}

func TestFallbackCredentialChangeKeepsSelectionAndDrainsRemoved(t *testing.T) {
	s, st, m, _ := fallbackFixture(t)
	ctx := context.Background()
	if err := s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	f, _, _ := st.ProFallbackConfig()
	f.AccountIDs = []int64{2}
	id, err := st.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-fallback", Provider: "sub2", Sub2: model.Sub2Settings{URL: m.server.URL, Email: f.Email}, Secret: "new-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveProFallbackConfig(f, id); err != nil {
		t.Fatal(err)
	}
	if err = s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	if m.on[1] || !m.on[2] || m.writes[2] != 1 {
		t.Fatal("credential rotation disrupted selection or cleanup", m.on, m.writes)
	}
	bindings, err := st.ProFallbackBindings()
	if err != nil || len(bindings) != 1 || bindings[0].ConnectionID != id {
		t.Fatal(bindings, err)
	}
}

func TestFallbackSourceFailureHoldRecoveryAndRestart(t *testing.T) {
	s, st, m, now := fallbackFixture(t)
	ctx := context.Background()
	m.sourceCount = 1
	if err := s.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	if m.on[1] || m.on[2] {
		t.Fatal("nonempty group")
	}
	m.sourceFailure = true
	m.sourceCount = 0
	if err := s.tickProFallback(ctx, true); err == nil {
		t.Fatal("failure hidden")
	}
	if m.on[1] || m.on[2] || s.proFallback.observation.Error == "" || *s.proFallback.observation.Count != 1 {
		t.Fatal("error treated as empty")
	}
	m.sourceFailure = false
	*now = now.Add(3 * time.Second)
	if err := s.tickProFallback(ctx, false); err != nil {
		t.Fatal(err)
	}
	if !m.on[1] || !m.on[2] {
		t.Fatal("automatic recovery failed")
	}
	m.sourceMissing = true
	if err := s.tickProFallback(ctx, true); err == nil {
		t.Fatal("missing group treated as empty")
	}
	if !m.on[1] || !m.on[2] {
		t.Fatal("missing group changed scheduling")
	}
	m.sourceMissing = false
	m.sourceCount = 1
	m.sourceIncomplete = true
	restarted := &Server{store: st}
	restarted.proFallback.now = func() time.Time { return *now }
	if err := restarted.tickProFallback(ctx, true); err == nil {
		t.Fatal("incomplete page accepted after restart")
	}
	if !m.on[1] || !m.on[2] || restarted.proFallback.observation.Count != nil {
		t.Fatal("restart acted on stale observation")
	}
	m.sourceIncomplete = false
	if err := restarted.tickProFallback(ctx, true); err != nil {
		t.Fatal(err)
	}
	if m.on[1] || m.on[2] {
		t.Fatal("nonempty recovery failed")
	}
}

func TestFallbackSourceGroupChangeInvalidatesObservation(t *testing.T) {
	s, st, m, _ := fallbackFixture(t)
	if err := s.tickProFallback(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	c, _ := st.ProRestockConfig()
	c.GroupID = 99
	if _, err := st.SaveProRestockConfig(c); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.getProFallback(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), `"phase":"unknown"`) || !strings.Contains(w.Body.String(), `"count":null`) {
		t.Fatal(w.Body.String())
	}
	if err := s.tickProFallback(context.Background(), true); err == nil {
		t.Fatal("missing new group accepted")
	}
	if !m.on[1] || !m.on[2] {
		t.Fatal("changed group disabled fallback without valid sample")
	}
	f, id, _ := st.ProFallbackConfig()
	f.Enabled = false
	if _, err := st.SaveProFallbackConfig(f, id); err != nil {
		t.Fatal(err)
	}
	if err := s.tickProFallback(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if m.on[1] || m.on[2] {
		t.Fatal("explicit disable must work even while source is unavailable")
	}
}
