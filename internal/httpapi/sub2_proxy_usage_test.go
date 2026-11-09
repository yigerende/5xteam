package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func TestSub2ProxyNewPushesUseFreshCurrentCountsBeforeLifetime(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	counts := map[int64]int{1: 0, 2: 1}
	var chosen []int64
	listCalls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin"}}`)
		case "/api/v1/admin/proxies/all":
			listCalls++
			fmt.Fprintf(w, `{"code":0,"data":[{"id":1,"host":"first.example","port":8080,"status":"active","account_count":%d},{"id":2,"host":"second.example","port":8080,"status":"active","account_count":%d}]}`, counts[1], counts[2])
		case "/api/v1/admin/accounts":
			var input struct {
				ProxyID int64 `json:"proxy_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || (input.ProxyID != 1 && input.ProxyID != 2) {
				t.Errorf("invalid proxy selection: %+v %v", input, err)
				http.Error(w, "invalid proxy", 400)
				return
			}
			chosen = append(chosen, input.ProxyID)
			counts[input.ProxyID]++
			fmt.Fprintf(w, `{"code":0,"data":{"id":%d}}`, 100+len(chosen))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	s := &Server{store: st, sub2: sub2.New()}
	cfg := model.Sub2Settings{URL: up.URL, Email: "admin@example.test", BindProxy: true, ProxyIDs: []int64{1, 2}}
	if _, err = st.Sub2ProxyUsages(up.URL, map[string]int64{
		sub2.ProxyBindingKey("http", "first.example", 8080, ""):  7,
		sub2.ProxyBindingKey("http", "second.example", 8080, ""): 2,
	}); err != nil {
		t.Fatal(err)
	}
	for i, scope := range []string{"team:new", "pro:new", "pro-siwc:new"} {
		if i == 2 {
			// A manual reassignment on the downstream must affect the next push.
			counts[1], counts[2] = 2, 0
		}
		if _, err = s.createSub2WithProxy(context.Background(), cfg, "secret", sub2.CreateAccountInput{Name: scope, GroupIDs: []int64{1}}, scope, "legacy", quietProxyLog); err != nil {
			t.Fatal(err)
		}
		s.completeSub2Push(cfg, scope)
	}
	if !reflect.DeepEqual(chosen, []int64{1, 2, 2}) || listCalls != 3 {
		t.Fatalf("current counts, lifetime tie-break or fresh lookup ignored: choices=%v listCalls=%d", chosen, listCalls)
	}
}

func TestSub2ProxySelectionUsesLifetimeWithinEqualCurrentCountsAndPending(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secondID := int64(2)
	uncertain := false
	var chosen []int64
	var keys []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin"}}`)
		case "/api/v1/admin/proxies/all":
			// Equal current counts allow lifetime usage to break the tie.
			fmt.Fprintf(w, `{"code":0,"data":[{"id":1,"host":"first.example","port":8080,"status":"active","account_count":0},{"id":%d,"host":"second.example","port":8080,"status":"active","account_count":0}]}`, secondID)
		case "/api/v1/admin/accounts":
			var input struct {
				ProxyID *int64 `json:"proxy_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.ProxyID == nil {
				t.Errorf("invalid create payload: %v", err)
				http.Error(w, "invalid payload", 400)
				return
			}
			chosen = append(chosen, *input.ProxyID)
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			if uncertain {
				http.Error(w, "timeout", 502)
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"id":%d}}`, 100+len(chosen))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	s := &Server{store: st, sub2: sub2.New()}
	cfg := model.Sub2Settings{URL: up.URL, Email: "admin@example.test", BindProxy: true, ProxyIDs: []int64{1, 2}}
	firstKey := sub2.ProxyBindingKey("http", "first.example", 8080, "")
	secondKey := sub2.ProxyBindingKey("http", "second.example", 8080, "")
	if _, err = st.Sub2ProxyUsages(up.URL, map[string]int64{firstKey: 6, secondKey: 2}); err != nil {
		t.Fatal(err)
	}
	create := func(scope string) error {
		_, e := s.createSub2WithProxy(context.Background(), cfg, "secret", sub2.CreateAccountInput{Name: scope, GroupIDs: []int64{1}}, scope, "legacy", quietProxyLog)
		return e
	}
	for _, scope := range []string{"team:first", "pro:reimported"} {
		if err = create(scope); err != nil {
			t.Fatal(err)
		}
		s.completeSub2Push(cfg, scope)
		secondID = 99
		cfg.ProxyIDs = []int64{1, 99}
	}
	if !reflect.DeepEqual(chosen, []int64{2, 99}) {
		t.Fatalf("equal current counts or reimport lost lifetime preference: %v", chosen)
	}
	usage, err := st.Sub2ProxyUsages(up.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetSub2ProxyUsage(up.URL, firstKey, 0, usage[firstKey].Revision); err != nil {
		t.Fatal(err)
	}
	uncertain = true
	if err = create("pro:uncertain"); err == nil {
		t.Fatal("expected ambiguous create")
	}
	// A pending create reserves current occupancy and lifetime use, even with an unlimited quota.
	if err = st.SetSub2ProxyUsage(up.URL, secondKey, 0, usage[secondKey].Revision); err != nil {
		t.Fatal(err)
	}
	uncertain = false
	if err = create("team:after-edit"); err != nil {
		t.Fatal(err)
	}
	// A fresh server retries the original request despite newer counts/selections.
	s = &Server{store: st, sub2: sub2.New()}
	cfg.ProxyIDs = []int64{99}
	if err = create("pro:uncertain"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chosen, []int64{2, 99, 1, 99, 1}) || keys[2] != keys[4] {
		t.Fatalf("selection or original request changed: %v", chosen)
	}
	usage, err = st.Sub2ProxyUsages(up.URL, nil)
	if err != nil || usage[firstKey].Count != 1 || usage[secondKey].Count != 1 || usage[firstKey].Pending != 0 {
		t.Fatalf("incorrect completed/pending counts: %+v, %v", usage, err)
	}
}

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
