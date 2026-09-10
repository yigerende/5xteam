package cpa

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestStatusOnlyReturnsUpstream401ForRelogin(t *testing.T) {
	var upstreamStatus = 200
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{map[string]any{"name": "a.json", "auth_index": "idx", "account_id": "acct"}}})
		case "/v0/management/api-call":
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": upstreamStatus, "body": map[string]any{"rate_limit": map[string]any{}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := New()
	settings := model.CPASettings{URL: ts.URL}
	status, err := c.Status(t.Context(), settings, "key", "a.json")
	if err != nil || status != http.StatusOK {
		t.Fatalf("healthy upstream status = %d, err=%v", status, err)
	}
	upstreamStatus = http.StatusUnauthorized
	status, err = c.Status(t.Context(), settings, "key", "a.json")
	if err != nil || status != http.StatusUnauthorized {
		t.Fatalf("upstream 401 status = %d, err=%v", status, err)
	}
}

func TestStatusDoesNotConvertCPAManagement401ToAccount401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"management key invalid"}`))
	}))
	defer ts.Close()
	status, err := New().Status(t.Context(), model.CPASettings{URL: ts.URL}, strings.Repeat("x", 4), "a.json")
	if err == nil || status != 0 {
		t.Fatalf("management 401 was converted to account status: status=%d err=%v", status, err)
	}
}

func TestRestoreSchedulingEnablesAndVerifiesAuthFile(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "disabled": false})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{map[string]any{"name": "a.json", "status": "active", "disabled": false, "unavailable": false}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	if err := New().RestoreScheduling(t.Context(), model.CPASettings{URL: ts.URL}, "key", "a.json"); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "a.json" || body["disabled"] != false {
		t.Fatalf("restore body = %#v", body)
	}
}

func TestWalkCPASnapshotsParsesRatioShape(t *testing.T) {
	var five, seven model.FreeQuotaWindow
	walkCPASnapshots(map[string]any{
		"gpt-5-codex": map[string]any{
			"used_ratio": 0.25,
			"window":     "primary",
		},
	}, &five, &seven)
	if seven.LimitWindowSeconds != 7*24*60*60 || seven.UsedPercent != 25 {
		t.Fatalf("unexpected weekly window: %+v", seven)
	}
	if five.LimitWindowSeconds != 0 {
		t.Fatalf("unexpected five-hour window: %+v", five)
	}

	five, seven = model.FreeQuotaWindow{}, model.FreeQuotaWindow{}
	walkCPASnapshots(map[string]any{
		"gpt-5-codex": map[string]any{
			"used_ratio": 0.75,
			"window":     "secondary",
		},
	}, &five, &seven)
	if seven.LimitWindowSeconds != 7*24*60*60 || seven.UsedPercent != 75 {
		t.Fatalf("unexpected seven-day window: %+v", seven)
	}
}

func TestWalkCPASnapshotsParsesRFC3339Reset(t *testing.T) {
	var five, seven model.FreeQuotaWindow
	walkCPASnapshots(map[string]any{
		"model": map[string]any{
			"used_ratio": 0.5,
			"window":     "weekly",
			"reset_at":   "2030-01-01T00:00:00Z",
		},
	}, &five, &seven)
	if seven.ResetAt <= 0 || seven.ResetAfterSeconds <= 0 {
		t.Fatalf("reset time was not parsed: %+v", seven)
	}
}

func TestParseRateLimitWindowsUsesDuration(t *testing.T) {
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":12,"limit_window_seconds":604800},"secondary_window":{"used_percent":34,"limit_window_seconds":18000}}}`)
	five, seven, ok := parseRateLimitWindows(body)
	if !ok || five.LimitWindowSeconds != 18000 || seven.LimitWindowSeconds != 604800 {
		t.Fatalf("windows were not classified by duration: five=%+v seven=%+v ok=%v", five, seven, ok)
	}
	if five.UsedPercent != 34 || seven.UsedPercent != 12 {
		t.Fatalf("unexpected usage values: five=%+v seven=%+v", five, seven)
	}
}

func TestParseRateLimitWindowsAcceptsCamelCaseAndResetInterval(t *testing.T) {
	body := []byte(`{"rateLimit":{"primaryWindow":{"usedRatio":0.2,"resetAfterSeconds":604800}}}`)
	_, seven, ok := parseRateLimitWindows(body)
	if !ok || seven.LimitWindowSeconds != 604800 || seven.UsedPercent != 20 {
		t.Fatalf("camelCase weekly window was not parsed: seven=%+v ok=%v", seven, ok)
	}
}
