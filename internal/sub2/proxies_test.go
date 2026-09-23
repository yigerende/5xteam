package sub2

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
)

func TestSelectProxyCountsAvailabilityAndTies(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Second), now.Add(time.Hour)
	zero, two, nine, negative := 0, 2, 9, -1
	proxies := []Proxy{
		{ID: 1, Status: "active", AccountCount: &nine},
		{ID: 2, Status: "active", AccountCount: &two, ExpiresAt: &future},
		{ID: 3, Status: "active", AccountCount: &two},
		{ID: 4, Status: "active", AccountCount: &zero, ExpiresAt: &past},
		{ID: 5, Status: "disabled", AccountCount: &zero},
		{ID: 6, Status: "active"},
		{ID: 7, Status: "active", AccountCount: &negative},
		{ID: 8, Status: "active", AccountCount: &zero},
	}
	seen := map[int64]bool{}
	for range 128 {
		p, ties, err := SelectProxy(proxies, []int64{1, 2, 2, 3, 4, 5, 6, 7}, now)
		if err != nil || ties != 2 || (p.ID != 2 && p.ID != 3) {
			t.Fatalf("selection=%+v ties=%d err=%v", p, ties, err)
		}
		seen[p.ID] = true
	}
	if len(seen) != 2 {
		t.Fatal("tied proxies must both be selectable")
	}
	for _, ids := range [][]int64{nil, {0, -1}, {4, 5, 6, 7, 99}} {
		if _, _, err := SelectProxy(proxies, ids, now); err == nil {
			t.Fatalf("invalid selection accepted: %v", ids)
		}
	}
	if p, ties, err := SelectProxy(proxies, []int64{1, 2}, now); err != nil || p.ID != 2 || ties != 1 {
		t.Fatal("minimum count not selected")
	}
}

func TestProxyAPIAndReauthorizationPayload(t *testing.T) {
	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin"}}`)
		case "/api/v1/admin/proxies/all":
			listCalls++
			if r.URL.Query().Get("with_count") != "true" || r.Header.Get("Authorization") != "Bearer admin" {
				t.Error("missing count flag or authentication")
			}
			fmt.Fprint(w, `{"code":0,"data":[{"id":2,"name":"low","status":"active","host":"127.0.0.1","port":8080,"account_count":0,"username":"private-user","password":"private-pass","expires_at":"2030-01-01T00:00:00Z"}]}`)
		case "/api/v1/admin/accounts", "/api/v1/admin/accounts/42/apply-oauth-credentials":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if strings.HasSuffix(r.URL.Path, "apply-oauth-credentials") {
				if _, exists := body["proxy_id"]; exists {
					t.Error("relogin changed proxy")
				}
			} else if body["proxy_id"] != float64(2) {
				t.Error("proxy_id missing from create root")
			}
			fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"account"}}`)
		default:
			t.Errorf("unexpected path %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New()
	settings := model.Sub2Settings{URL: server.URL, Email: "admin@example.test", BindProxy: true, ProxyIDs: []int64{2}}
	proxies, err := client.Proxies(context.Background(), settings, "secret")
	if err != nil || len(proxies) != 1 || proxies[0].AccountCount == nil || *proxies[0].AccountCount != 0 {
		t.Fatalf("list: %v %v", proxies, err)
	}
	encoded, _ := json.Marshal(proxies)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "password") {
		t.Fatal("proxy credentials leaked")
	}
	id := int64(2)
	if _, err := client.CreateAccount(context.Background(), settings, "secret", CreateAccountInput{Name: "account", GroupIDs: []int64{1}, ProxyID: &id}, "create-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ApplyOAuthCredentials(context.Background(), settings, "secret", 42, map[string]any{"access_token": "new-at", "refresh_token": "new-rt"}); err != nil {
		t.Fatal(err)
	}
	if listCalls != 1 {
		t.Fatal("reauthorization must not query proxies")
	}
}

func TestSub2EndpointKey(t *testing.T) {
	for _, raw := range []string{" HTTPS://SUB.example:443/ ", "https://sub.example", "https://sub.example/"} {
		key, err := EndpointKey(raw)
		if err != nil || key != "https://sub.example" {
			t.Fatalf("key=%q err=%v", key, err)
		}
	}
}
