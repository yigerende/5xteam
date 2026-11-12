package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

type proxyReleaseRemote struct {
	mu                  sync.Mutex
	proxy               int64
	gets, puts, creates int
	mode                string
}

func proxyReleaseFixture(t *testing.T, minutes int, due bool) (*Server, *proxyReleaseRemote, model.Sub2ProxyBinding, string, model.Sub2Settings) {
	t.Helper()
	remote := &proxyReleaseRemote{proxy: 7}
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote.mu.Lock()
		defer remote.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture-admin"}}`)
		case r.URL.Path == "/api/v1/admin/proxies/all":
			fmt.Fprint(w, `{"code":0,"data":[{"id":7,"protocol":"http","host":"proxy.example","port":8080,"status":"active","account_count":0}]}`)
		case r.URL.Path == "/api/v1/admin/accounts" && r.Method == "POST":
			remote.creates++
			fmt.Fprint(w, `{"code":0,"data":{"id":89,"name":"fixture"}}`)
		case r.URL.Path == "/api/v1/admin/accounts/88" && r.Method == "GET":
			remote.gets++
			if remote.mode == "unauthorized" {
				http.Error(w, `{"message":"denied"}`, 403)
				return
			}
			if remote.mode == "missing" {
				fmt.Fprint(w, `{"code":0,"data":{"id":88}}`)
				return
			}
			if remote.mode == "deleted" {
				http.Error(w, `{"message":"account not found"}`, 404)
				return
			}
			if remote.mode == "route404" {
				http.Error(w, `{"message":"route not found"}`, 404)
				return
			}
			if remote.proxy == 0 {
				fmt.Fprint(w, `{"code":0,"data":{"id":88,"proxy_id":null}}`)
			} else {
				fmt.Fprintf(w, `{"code":0,"data":{"id":88,"proxy_id":%d}}`, remote.proxy)
			}
		case r.URL.Path == "/api/v1/admin/accounts/88" && r.Method == "PUT":
			remote.puts++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body) != 1 || body["proxy_id"] != float64(0) {
				t.Errorf("unrelated fields sent: %#v", body)
			}
			if remote.mode != "ignored" {
				remote.proxy = 0
			}
			if remote.mode == "lost" {
				http.Error(w, `{"message":"response lost"}`, 502)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"id":88}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(web.Close)
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, sub2: sub2.New()}
	t.Cleanup(func() { _ = s.store.Close() })
	v := model.Sub2Settings{URL: web.URL, Email: "admin@example.test", GroupIDs: []int64{1}, BindProxy: true, ProxyIDs: []int64{7}, ProxyReleaseMinutes: minutes}
	target, err := st.SaveDownstreamTarget(model.DownstreamTarget{Provider: "sub2", Scope: "sub2-proxy-release", Sub2: v, Secret: "fixture-secret"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	b := model.Sub2ProxyBinding{ID: "binding-fixture", Endpoint: web.URL, TargetID: target, Scope: "pro:fixture", AccountID: 88, ProxyID: 7, Minutes: minutes, BoundAt: now}
	if minutes > 0 {
		at := now.Add(time.Duration(minutes) * time.Minute)
		if due {
			at = now.Add(-time.Second)
		}
		b.DueAt = &at
	}
	if err = st.CompleteSub2ProxyBinding("fixture-attempt", `{}`, "fixture", 88, b); err != nil {
		t.Fatal(err)
	}
	return s, remote, b, dir, v
}

func proxyBindingState(t *testing.T, s *Server, id string) model.Sub2ProxyBinding {
	t.Helper()
	rows, err := s.store.Sub2ProxyBindings([]string{id})
	if err != nil {
		t.Fatal(err)
	}
	b, ok := rows[id]
	if !ok {
		t.Fatal("missing binding")
	}
	return b
}

func TestSub2ProxyReleaseModesAndVerification(t *testing.T) {
	for _, tc := range []struct {
		name, mode, want string
		minutes          int
		due              bool
		proxy            int64
		puts             int
	}{
		{"permanent", "", "permanent", 0, false, 7, 0}, {"countdown", "", "bound", 30, false, 7, 0},
		{"due", "", "unbound", 30, true, 7, 1}, {"already removed", "", "unbound", 30, true, 0, 0},
		{"manually changed", "", "changed", 30, true, 9, 0}, {"missing field", "missing", "retry", 30, true, 7, 0},
		{"forbidden", "unauthorized", "retry", 30, true, 7, 0}, {"ignored update", "ignored", "retry", 30, true, 7, 1},
		{"account deleted", "deleted", "deleted", 30, true, 7, 0}, {"missing route", "route404", "retry", 30, true, 7, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, b, _, _ := proxyReleaseFixture(t, tc.minutes, tc.due)
			r.mode, r.proxy = tc.mode, tc.proxy
			before, err := s.store.Sub2ProxyUsages(b.Endpoint, map[string]int64{"existing": 9})
			if err != nil {
				t.Fatal(err)
			}
			s.tickSub2ProxyReleases(context.Background())
			after, err := s.store.Sub2ProxyUsages(b.Endpoint, nil)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("release changed cumulative usage", before, after, err)
			}
			got := proxyBindingState(t, s, b.ID)
			if got.State != tc.want || r.puts != tc.puts {
				t.Fatalf("%s PUT=%d", got.State, r.puts)
			}
			if !got.BoundAt.Equal(b.BoundAt) {
				t.Fatal("binding clock reset")
			}
			if got.Error != "" && got.State != "retry" {
				t.Fatal(got.Error)
			}
		})
	}
}

func TestSub2ProxyReleaseLostResponseRestartAndPinnedConnection(t *testing.T) {
	s, r, b, dir, v := proxyReleaseFixture(t, 5, true)
	r.mode = "lost"
	s.tickSub2ProxyReleases(context.Background())
	got := proxyBindingState(t, s, b.ID)
	if got.State != "retry" || r.puts != 1 {
		t.Fatal(got.State, r.puts)
	}
	// A manual retry only advances the local due queue; it performs no network I/O.
	if err := s.store.RetrySub2ProxyRelease(b.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	v.URL = "http://127.0.0.1:1"
	if _, err := s.store.SaveSub2Settings(v, "other-secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s.store, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.sub2ProxyReleaseClients = sync.Map{}
	s.tickSub2ProxyReleases(context.Background())
	got = proxyBindingState(t, s, b.ID)
	if got.State != "unbound" || r.puts != 1 || !got.DueAt.Equal(*b.DueAt) {
		t.Fatal("replayed PUT or reset deadline", got, r.puts)
	}
	s.tickSub2ProxyReleases(context.Background())
	if r.puts != 1 {
		t.Fatal("completed task replayed")
	}
}

func TestSub2ProxyReleaseSuccessfulCreatePersistsTimerAndUsageOnce(t *testing.T) {
	s, r, _, _, v := proxyReleaseFixture(t, 5, false)
	input := sub2.CreateAccountInput{Name: "fixture", Credentials: map[string]any{"access_token": "fixture"}, GroupIDs: []int64{1}, Concurrency: 1}
	created, err := s.createSub2WithProxy(context.Background(), v, "fixture-secret", input, "pro:new", "legacy", nil)
	if err != nil {
		t.Fatal(err)
	}
	b := proxyBindingState(t, s, created.ProxyBindingID)
	if b.Minutes != 5 || b.ProxyID != 7 || b.DueAt == nil || b.DueAt.Sub(b.BoundAt) != 5*time.Minute {
		t.Fatal(b)
	}
	v.ProxyReleaseMinutes = 50
	retried, err := s.createSub2WithProxy(context.Background(), v, "fixture-secret", input, "pro:new", "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ProxyBindingID != created.ProxyBindingID || r.creates != 1 {
		t.Fatal("create replayed")
	}
	latest := proxyBindingState(t, s, created.ProxyBindingID)
	if !latest.DueAt.Equal(*b.DueAt) {
		t.Fatal("deadline extended")
	}
}

func TestSub2ProxyReleaseStatusAPIAndRetry(t *testing.T) {
	s, r, b, _, _ := proxyReleaseFixture(t, 5, true)
	r.mode = "missing"
	s.tickSub2ProxyReleases(context.Background())
	mux := http.NewServeMux()
	s.registerSub2ProxyRelease(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sub2-proxy-bindings?ids="+b.ID, nil))
	var body struct {
		Data struct {
			Items map[string]map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	row := body.Data.Items[b.ID]
	if w.Code != 200 || row["state"] != "retry" || row["target_id"] != nil || row["endpoint"] != nil {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/sub2-proxy-bindings/"+b.ID+"/retry", nil))
	if w.Code != 200 || r.puts != 0 {
		t.Fatal(w.Body.String())
	}
}

func TestSub2ProxyReleaseSavedPolicyBeforeUnknownCreate(t *testing.T) {
	s, r, _, _, v := proxyReleaseFixture(t, 5, false)
	input := sub2.CreateAccountInput{Name: "fixture", Credentials: map[string]any{"access_token": "fixture"}, GroupIDs: []int64{1}, Concurrency: 1}
	target, err := s.store.SaveDownstreamTarget(model.DownstreamTarget{Provider: "sub2", Scope: "sub2-proxy-release", Sub2: v, Secret: "fixture-secret"})
	if err != nil {
		t.Fatal(err)
	}
	proxy := int64(7)
	input.ProxyID = &proxy
	pending := sub2PushAttempt{Input: input, Key: "saved-key", StartedAt: time.Now(), Retention: &sub2ProxyRetention{TargetID: target, Minutes: 5}}
	raw, _ := json.Marshal(pending)
	if err = s.store.SaveSub2PushAttempt(sub2PushAttemptID(v.URL, "pro:pending"), string(raw)); err != nil {
		t.Fatal(err)
	}
	v.ProxyReleaseMinutes = 60
	created, err := s.createSub2WithProxy(context.Background(), v, "fixture-secret", input, "pro:pending", "unused", nil)
	if err != nil {
		t.Fatal(err)
	}
	b := proxyBindingState(t, s, created.ProxyBindingID)
	if b.Minutes != 5 || b.TargetID != target || r.creates != 1 {
		t.Fatal("pending policy changed", b)
	}
}
