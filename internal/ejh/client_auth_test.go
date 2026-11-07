package ejh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestExpiredTokenQueryRefreshAndMutationSafety(t *testing.T) {
	for _, tc := range []struct {
		name, method, message  string
		alwaysReject           bool
		wantAuth, wantBusiness int32
		wantError              bool
	}{
		{"balance", "account.balance.info", "token不正确", false, 2, 2, false},
		{"card_balance", "card.balance.info", "token不正确", false, 2, 2, false},
		{"trades", "card.trade.info", "token已过期", false, 2, 2, false},
		{"bounded", "card.list.info", "token不正确", true, 2, 2, true},
		{"open_not_repeated", "card.open.info", "token不正确", false, 1, 1, true},
		{"cancel_not_repeated", "card.cancel.info", "token不正确", false, 1, 1, true},
		{"generic_not_repeated", "account.balance.info", "请求失败", false, 1, 1, true},
		{"disabled_not_repeated", "card.list.info", "接口已停用，无法访问！", false, 1, 1, true},
		{"disabled_sensitive_suffix", "card.list.info", "接口已停用，无法访问！secret-card-number", false, 1, 1, true},
		{"sensitive_message", "account.balance.info", "token不正确 secret-card-number", false, 1, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var auth, business atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v3/authenticate" {
					json.NewEncoder(w).Encode(map[string]any{"code": "000000", "data": map[string]any{"authToken": fmt.Sprintf("token-%d", auth.Add(1)), "validity": 1800000}})
					return
				}
				business.Add(1)
				if r.Header.Get("Authorization") == "token-1" || tc.alwaysReject {
					json.NewEncoder(w).Encode(map[string]any{"code": "000009", "msg": tc.message})
					return
				}
				fmt.Fprint(w, `{"code":"000000","data":{"balance":"12.34"}}`)
			}))
			defer up.Close()
			c := New()
			var out map[string]string
			err := c.Call(context.Background(), Settings{URL: up.URL, AppKey: "fixture"}, "fixture-secret", tc.method, map[string]any{}, &out)
			if (err != nil) != tc.wantError || auth.Load() != tc.wantAuth || business.Load() != tc.wantBusiness {
				t.Fatalf("err=%v auth=%d business=%d", err, auth.Load(), business.Load())
			}
			if err != nil && strings.Contains(err.Error(), "secret-card-number") {
				t.Fatal("supplier message leaked")
			}
			if tc.name == "disabled_not_repeated" && !strings.Contains(err.Error(), "请联系 EJH 恢复 API 服务") {
				t.Fatal("disabled API reason hidden", err)
			}
			if !tc.wantError && out["balance"] != "12.34" {
				t.Fatal("recovered response lost")
			}
			if tc.name == "bounded" || tc.name == "open_not_repeated" || tc.name == "cancel_not_repeated" {
				if c.token != "" {
					t.Fatal("invalid cached token retained")
				}
			}
		})
	}
}

func TestDisabledAPIAuthenticationReportsReason(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v3/authenticate" {
			t.Error("business request sent after authentication failed")
		}
		fmt.Fprint(w, `{"code":"000009","msg":"接口已停用，无法访问！"}`)
	}))
	defer up.Close()
	c := New()
	err := c.Call(context.Background(), Settings{URL: up.URL, AppKey: "fixture"}, "fixture-secret", "card.list.info", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "请联系 EJH 恢复 API 服务") || calls.Load() != 1 || c.token != "" {
		t.Fatalf("disabled API: err=%v calls=%d", err, calls.Load())
	}
}

func TestConcurrentExpiredTokenRefresh(t *testing.T) {
	var auth, rejected atomic.Int32
	const parallel = 8
	barrier := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/authenticate" {
			json.NewEncoder(w).Encode(map[string]any{"code": "000000", "data": map[string]any{"authToken": fmt.Sprintf("token-%d", auth.Add(1)), "validity": 1800000}})
			return
		}
		if r.Header.Get("Authorization") == "token-1" {
			if rejected.Add(1) == parallel {
				close(barrier)
			}
			<-barrier
			fmt.Fprint(w, `{"code":"000009","msg":"token不正确"}`)
			return
		}
		fmt.Fprint(w, `{"code":"000000","data":{}}`)
	}))
	defer up.Close()
	c := New()
	cfg := Settings{URL: up.URL, AppKey: "fixture"}
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Call(context.Background(), cfg, "fixture-secret", "card.list.info", map[string]any{}, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if auth.Load() != 2 {
		t.Fatalf("refresh stampede: auth=%d", auth.Load())
	}
	c.invalidateToken(Settings{URL: up.URL, AppKey: "other-account"}, "fixture-secret", "token-2")
	if c.token != "token-2" {
		t.Fatal("different account invalidated current token")
	}
}
