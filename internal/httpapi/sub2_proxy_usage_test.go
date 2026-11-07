package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func TestSub2ProxyCumulativeReimportManualEditAndPending(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	proxyID := int64(1)
	creates := 0
	uncertain := false
	rejected := false
	var keys []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin"}}`)
		case "/api/v1/admin/proxies/all":
			fmt.Fprintf(w, `{"code":0,"data":[{"id":%d,"name":"renamed","protocol":"http","host":"proxy.example","port":8080,"username":"private-user","password":"private-password","status":"active","account_count":0}]}`, proxyID)
		case "/api/v1/admin/accounts":
			creates++
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			if rejected {
				http.Error(w, "invalid input", 400)
				return
			}
			if uncertain {
				http.Error(w, "upstream timeout", 502)
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"id":%d}}`, creates)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	s := &Server{store: st, sub2: sub2.New()}
	cfg := model.Sub2Settings{URL: up.URL, Email: "admin@test.example", BindProxy: true, ProxyIDs: []int64{1}}
	if err = st.SaveSub2ProxyBindingLimit(up.URL, 1); err != nil {
		t.Fatal(err)
	}
	create := func(scope string) error {
		_, e := s.createSub2WithProxy(context.Background(), cfg, "secret", sub2.CreateAccountInput{Name: scope, GroupIDs: []int64{1}}, scope, "legacy", quietProxyLog)
		return e
	}
	if err = create("first"); err != nil {
		t.Fatal(err)
	}
	s.completeSub2Push(cfg, "first")
	proxyID = 99
	cfg.ProxyIDs = []int64{99} // upstream deletion/reimport changed ID; live count is zero
	if err = create("second"); err == nil || creates != 1 {
		t.Fatal("reimport reset cap", err, creates)
	}
	proxies, err := s.sub2.Proxies(context.Background(), cfg, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.decorateSub2ProxyUsage(up.URL, proxies); err != nil {
		t.Fatal(err)
	}
	p := proxies[0]
	if p.CumulativeBindings != 1 || p.Available(time.Now()) {
		t.Fatal(p)
	}
	encoded, _ := json.Marshal(p)
	if strings.Contains(string(encoded), "private-") {
		t.Fatal("proxy credentials leaked")
	}
	if err = st.SetSub2ProxyUsage(up.URL, p.BindingKey, 0, p.BindingRevision); err != nil {
		t.Fatal(err)
	}
	uncertain = true
	if err = create("second"); err == nil {
		t.Fatal("expected uncertain response")
	}
	if err = create("third"); err == nil || creates != 2 {
		t.Fatal("pending slot not held", err, creates)
	}
	// A newly lowered cap must not block query/replay of the original request.
	if err = st.SetSub2ProxyUsage(up.URL, p.BindingKey, 8, p.BindingRevision+1); err != nil {
		t.Fatal(err)
	}
	uncertain = false
	if err = create("second"); err != nil {
		t.Fatal(err)
	}
	if keys[1] != keys[2] {
		t.Fatal("uncertain retry changed idempotency key")
	}
	if err = create("second"); err != nil || creates != 3 {
		t.Fatal("acknowledged result replayed", err, creates)
	}
	s.completeSub2Push(cfg, "second")
	if err = s.decorateSub2ProxyUsage(up.URL, proxies); err != nil {
		t.Fatal(err)
	}
	if proxies[0].CumulativeBindings != 9 || proxies[0].PendingBindings != 0 {
		t.Fatal("retry double counted", proxies[0])
	}
	if err = st.SetSub2ProxyUsage(up.URL, p.BindingKey, 0, proxies[0].BindingRevision); err != nil {
		t.Fatal(err)
	}
	rejected = true
	if err = create("rejected"); err == nil {
		t.Fatal("expected rejection")
	}
	rejected = false
	if err = create("after-rejection"); err != nil {
		t.Fatal("explicit rejection left quota occupied", err)
	}
	if err = s.decorateSub2ProxyUsage(up.URL, proxies); err != nil {
		t.Fatal(err)
	}
	if proxies[0].CumulativeBindings != 1 || proxies[0].PendingBindings != 0 {
		t.Fatal("rejected request was counted", proxies[0])
	}
}

func TestSub2ProxyUsageEndpointsRejectInvalidOrStaleEdits(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st}
	for _, value := range []any{-1, 1.5, nil} {
		r := payCall(t, s.saveSub2ProxyBindingPolicy, "", "", map[string]any{"url": "https://sub.example", "max_bindings": value})
		if r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if r := payCall(t, s.saveSub2ProxyBindingPolicy, "", "", map[string]any{"url": "HTTPS://SUB.EXAMPLE:443/", "max_bindings": 5}); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if limit, err := st.Sub2ProxyBindingLimit("https://sub.example"); err != nil || limit != 5 {
		t.Fatal(limit, err)
	}
	if _, err = st.Sub2ProxyUsages("https://sub.example", map[string]int64{"proxy": 1}); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"url": "https://sub.example/", "binding_key": "proxy", "count": 12, "expected_revision": 0}
	if r := payCall(t, s.editSub2ProxyBindingUsage, "", "", input); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r := payCall(t, s.editSub2ProxyBindingUsage, "", "", input); r.Code != 409 {
		t.Fatal("stale edit accepted", r.Body.String())
	}
}

func TestSub2ProxyUsageRequiresSessionAndSameOrigin(t *testing.T) {
	s := &Server{sessions: map[string]time.Time{"valid": time.Now().Add(time.Hour)}}
	called := 0
	handler := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(200) }))
	for _, path := range []string{"/api/sub2-proxy-binding-policy", "/api/sub2-proxy-binding-usage"} {
		for _, kind := range []string{"anonymous", "cross-origin", "json-required", "valid"} {
			r := httptest.NewRequest("PUT", path, strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			if kind != "anonymous" {
				r.AddCookie(&http.Cookie{Name: authCookieName, Value: "valid"})
			}
			if kind == "cross-origin" {
				r.Header.Set("Origin", "https://another.example")
			}
			if kind == "json-required" {
				r.Header.Del("Content-Type")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := map[string]int{"anonymous": 401, "cross-origin": 403, "json-required": 415, "valid": 200}[kind]
			if w.Code != want {
				t.Fatalf("%s %s: %d", path, kind, w.Code)
			}
		}
	}
	if called != 2 {
		t.Fatal("invalid usage write reached handler", called)
	}
}
