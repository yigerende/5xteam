package workflow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

const gptPlanFixture = `{"accounts":{"default":{"account":{"plan_type":"free"},"entitlement":{"subscription_plan":"chatgptproplan","has_active_subscription":true}}}}`
const gptMeFixture = `{"created":1700000000,"plan_type":"plus"}`

func gptInfoTestToken() string {
	raw, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "free", "chatgpt_user_id": "user-1"}, "https://api.openai.com/profile": map[string]any{"email": "one@example.com"}})
	return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}
func TestRefreshGPTInfoOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		plan, me                 gptInfoResponse
		status, planType, source string
		created                  bool
		planAttempts, meAttempts int
	}{
		{"both", gptInfoResponse{Status: 200, Body: gptPlanFixture}, gptInfoResponse{Status: 200, Body: gptMeFixture}, "success", "pro", "entitlement", true, 1, 1},
		{"me_403", gptInfoResponse{Status: 200, Body: gptPlanFixture}, gptInfoResponse{Status: 403, Body: "denied"}, "partial", "pro", "entitlement", false, 1, 1},
		{"me_fallback", gptInfoResponse{Status: 403}, gptInfoResponse{Status: 200, Body: gptMeFixture}, "partial", "plus", "me", true, 1, 1},
		{"jwt_only", gptInfoResponse{Status: 401}, gptInfoResponse{Status: 401}, "failed", "free", "jwt", false, 1, 1},
		{"missing_created", gptInfoResponse{Status: 200, Body: gptPlanFixture}, gptInfoResponse{Status: 200, Body: `{"name":"test"}`}, "partial", "pro", "entitlement", false, 1, 1},
		{"invalid_json", gptInfoResponse{Status: 200, Body: "html"}, gptInfoResponse{Status: 200, Body: "null"}, "failed", "free", "jwt", false, 1, 1},
		{"wrong_identity", gptInfoResponse{Status: 401}, gptInfoResponse{Status: 200, Body: `{"email":"another@example.com","created":1700000000}`}, "failed", "free", "jwt", false, 1, 1},
		{"rate_limited", gptInfoResponse{Status: 429}, gptInfoResponse{Status: 200, Body: gptMeFixture}, "partial", "plus", "me", true, 3, 1},
		{"cf_limited", gptInfoResponse{Status: 403, Body: "<html>Just a moment cloudflare</html>"}, gptInfoResponse{Status: 200, Body: gptMeFixture}, "partial", "plus", "me", true, 3, 1},
		{"cf_header_only", gptInfoResponse{Status: 200, Body: gptPlanFixture}, gptInfoResponse{Status: 403, Body: "<html>Verify your request</html>", CFMitigated: "challenge", ContentType: "text/html", CFRay: "test-ray-HKG"}, "partial", "pro", "entitlement", false, 1, 3},
		{"network", gptInfoResponse{Error: "timeout"}, gptInfoResponse{Status: 401}, "failed", "free", "jwt", false, 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := refreshGPTInfo(context.Background(), gptInfoTestToken(), func(_ context.Context, path string) gptInfoResponse {
				if strings.HasPrefix(path, accountPlanCheckPath) {
					return tc.plan
				}
				return tc.me
			}, func(context.Context, time.Duration) error { return nil })
			if result.Check.Status != tc.status || result.Plan.CurrentPlanType != tc.planType || result.Check.PlanSource != tc.source || (result.CreatedAtOpenAI != nil) != tc.created || result.Check.Plan.Attempts != tc.planAttempts || result.Check.Me.Attempts != tc.meAttempts {
				t.Fatalf("unexpected result: %+v", result)
			}
			if tc.created && result.CreatedAtOpenAI.Unix() != 1700000000 {
				t.Fatal("incorrect seconds conversion")
			}
			if tc.name == "cf_header_only" && (result.Check.Me.CFMitigated != "challenge" || result.Check.Me.CFRay != "test-ray-HKG" || !strings.Contains(result.Check.Me.Error, "Cloudflare")) {
				t.Fatalf("CF diagnostics lost: %+v", result.Check.Me)
			}
		})
	}
}
func TestRefreshGPTInfoConcurrentRequestsAndOnlyRetryFailedEndpoint(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan model.MailGPTInfoResult, 1)
	go func() {
		done <- refreshGPTInfo(context.Background(), gptInfoTestToken(), func(_ context.Context, path string) gptInfoResponse {
			mu.Lock()
			calls[path]++
			n := calls[path]
			mu.Unlock()
			if n == 1 {
				started <- path
				<-release
			}
			if path == "/me" {
				return gptInfoResponse{Status: 200, Body: gptMeFixture}
			}
			if n == 1 {
				return gptInfoResponse{Status: 429, RetryAfter: "7"}
			}
			return gptInfoResponse{Status: 200, Body: gptPlanFixture}
		}, func(_ context.Context, d time.Duration) error {
			if d != 7*time.Second {
				t.Errorf("Retry-After=%v", d)
			}
			return nil
		})
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("endpoints not concurrent")
		}
	}
	close(release)
	result := <-done
	if result.Check.Status != "success" || result.Check.Plan.Attempts != 2 || result.Check.Me.Attempts != 1 {
		t.Fatal(result)
	}
}
func TestGPTInfoLongRetryAfterAndCancellation(t *testing.T) {
	for _, retryAfter := range []string{"86400", "9223372036854775807", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)} {
		_, status := fetchGPTInfo(context.Background(), "/me", func(context.Context, string) gptInfoResponse {
			return gptInfoResponse{Status: 429, RetryAfter: retryAfter}
		}, func(context.Context, time.Duration) error { t.Fatal("must respect long cooldown"); return nil })
		if status.Attempts != 1 {
			t.Fatal(status)
		}
	}
	_, check := fetchGPTInfo(context.Background(), "/me", func(context.Context, string) gptInfoResponse { return gptInfoResponse{Status: 429, RetryAfter: "120"} }, func(context.Context, time.Duration) error { t.Fatal("must not retry before cooldown"); return nil })
	if check.Attempts != 1 {
		t.Fatal(check)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, check = fetchGPTInfo(ctx, "/me", func(context.Context, string) gptInfoResponse {
		t.Fatal("request after cancel")
		return gptInfoResponse{}
	}, waitGPTInfo)
	if check.Attempts != 0 || check.Error == "" {
		t.Fatal(check)
	}
}
func TestGPTInfoMultipleSpacesAndCreatedValidation(t *testing.T) {
	var payload map[string]any
	_ = json.Unmarshal([]byte(`{"accounts":{"a":{"account":{"plan_type":"free"},"entitlement":{"subscription_plan":"chatgptplusplan"}},"b":{"account":{"plan_type":"team"}}}}`), &payload)
	plan, err := parseGPTInfoPlan(payload, model.UserInfo{AccountID: "a", PlanType: "pro"})
	if err != nil || plan.CurrentPlanType != "plus" {
		t.Fatal(plan, err)
	}
	if _, err = parseGPTInfoPlan(payload, model.UserInfo{}); err == nil {
		t.Fatal("picked arbitrary Team")
	}
	for _, v := range []any{nil, "", 0, -1, "1700000000000", "abc", "1700000000.5"} {
		if parseGPTCreated(v) != nil {
			t.Fatalf("accepted invalid created %v", v)
		}
	}
}
func TestGPTInfoRequiresATAndProxy(t *testing.T) {
	c := &Client{}
	if r := c.RefreshAccountInfo(context.Background(), ""); r.Check.Status != "failed" {
		t.Fatal(r)
	}
	if r := c.RefreshAccountInfo(context.Background(), "token"); !strings.Contains(r.Check.Error, "代理") {
		t.Fatal(r)
	}
}

// Opt in locally: exercises the actual curl_cffi adapter against a local proxy,
// without sending any request or credential to OpenAI.
func TestGPTInfoCurlTransport(t *testing.T) {
	if os.Getenv("GPT_INFO_CURL_TEST") != "1" {
		t.Skip("set GPT_INFO_CURL_TEST=1 for local curl_cffi integration")
	}
	// Host environment exemptions must never bypass the configured proxy.
	t.Setenv("NO_PROXY", "*")
	t.Setenv("no_proxy", "*")
	var calls, meCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Accept") != "*/*" || r.Header.Get("Referer") != "https://chatgpt.com/" || r.Header.Get("Authorization") != "Bearer "+gptInfoTestToken() {
			t.Errorf("incorrect request headers")
		}
		if !strings.Contains(r.UserAgent(), "Chrome/136.") || !strings.Contains(r.Header.Get("sec-ch-ua"), `v="136"`) {
			t.Errorf("fingerprint UA: %s", r.UserAgent())
		}
		for key, expected := range map[string]string{
			"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8", "Origin": "https://chatgpt.com", "oai-language": "zh-CN",
			"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin",
			"oai-device-id": stablePlanDeviceID("one@example.com"), "x-openai-target-path": r.URL.Path, "x-openai-target-route": r.URL.Path,
		} {
			if r.Header.Get(key) != expected {
				t.Errorf("header %s: got %q want %q", key, r.Header.Get(key), expected)
			}
		}
		switch r.URL.Path {
		case "/backend-api/me":
			if meCalls.Add(1) == 1 {
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("cf-mitigated", "challenge")
				w.Header().Set("cf-ray", "fixture-ray-HKG")
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte("<html>Verify your request</html>"))
				return
			}
			w.Write([]byte(gptMeFixture))
		case "/backend-api/accounts/check/v4-2023-04-27":
			if r.URL.Query().Get("timezone_offset_min") != "-480" {
				t.Error("missing timezone")
			}
			w.Write([]byte(gptPlanFixture))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer proxy.Close()
	c, err := NewClient(model.Settings{BaseURL: "http://127.0.0.1:1/backend-api", ProxyURL: proxy.URL, RequestTimeoutSeconds: 20})
	if err != nil {
		t.Fatal(err)
	}
	result := c.RefreshAccountInfo(context.Background(), gptInfoTestToken())
	if result.Check.Status != "success" || calls.Load() != 3 || result.Check.Me.Attempts != 2 || result.Check.Plan.Attempts != 1 {
		t.Fatalf("result=%+v calls=%d", result, calls.Load())
	}
}
