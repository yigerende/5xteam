package sub2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestClientLoginGroupsCreateAndQuota(t *testing.T) {
	var loginCalls atomic.Int32
	var createdBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCalls.Add(1)
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input["email"] != "admin@example.com" || input["password"] != "secret" {
				t.Errorf("unexpected login input: %+v", input)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"admin-token"}}`))
		case "/api/v1/admin/groups/all":
			if r.Header.Get("Authorization") != "Bearer admin-token" {
				t.Errorf("missing bearer token")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":44,"name":"Free pool","platform":"openai"},{"id":8,"name":"Claude","platform":"anthropic"}]}`))
		case "/api/v1/admin/accounts":
			if r.Header.Get("Idempotency-Key") != "free-pipeline-account-1" {
				t.Errorf("missing idempotency key")
			}
			_ = json.NewDecoder(r.Body).Decode(&createdBody)
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":101,"name":"free@example.com"}}`))
		case "/api/v1/admin/openai/accounts/101/quota":
			_, _ = w.Write([]byte(`{"code":0,"data":{"rate_limit":{"primary_window":{"used_percent":55.5,"limit_window_seconds":18000,"reset_after_seconds":30},"secondary_window":{"used_percent":100,"limit_window_seconds":604800,"reset_at":12345}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	settings := model.Sub2Settings{URL: server.URL, Email: "admin@example.com", GroupIDs: []int64{44, 45}, GroupNames: []string{"Free pool", "Backup"}, Models: []string{"gpt-5.2-codex", "gpt-5.1-codex-mini"}, AccountConcurrency: 12, CpaWS: true}
	client := New()
	groups, err := client.Groups(context.Background(), settings, "secret")
	if err != nil || len(groups) != 1 || groups[0].ID != 44 {
		t.Fatalf("unexpected groups: %+v err=%v", groups, err)
	}
	account, err := client.CreateAccount(context.Background(), settings, "secret", CreateAccountInput{
		Name: "free@example.com", GroupIDs: settings.GroupIDs, Models: settings.Models, Concurrency: 12,
		CpaWS:       true,
		Credentials: map[string]any{"access_token": "oauth-access", "refresh_token": "oauth-refresh", "chatgpt_account_id": "account-1"},
	}, "free-pipeline-account-1")
	if err != nil || account.ID != 101 {
		t.Fatalf("unexpected created account: %+v err=%v", account, err)
	}
	if createdBody["platform"] != "openai" || createdBody["type"] != "oauth" || createdBody["confirm_mixed_channel_risk"] != true || createdBody["concurrency"] != float64(12) {
		t.Fatalf("unexpected create body: %+v", createdBody)
	}
	if createdBody["cpa_ws"] != float64(1) {
		t.Fatalf("cpa_ws should be numeric 1 when enabled: %+v", createdBody["cpa_ws"])
	}
	groupIDs, ok := createdBody["group_ids"].([]any)
	if !ok || len(groupIDs) != 2 || groupIDs[0] != float64(44) || groupIDs[1] != float64(45) {
		t.Fatalf("unexpected group_ids: %#v", createdBody["group_ids"])
	}
	credentials, ok := createdBody["credentials"].(map[string]any)
	if !ok || credentials["access_token"] != "oauth-access" || credentials["refresh_token"] != "oauth-refresh" || credentials["chatgpt_account_id"] != "account-1" {
		t.Fatalf("unexpected credentials: %#v", createdBody["credentials"])
	}
	mapping, ok := credentials["model_mapping"].(map[string]any)
	if !ok || mapping["gpt-5.2-codex"] != "gpt-5.2-codex" || mapping["gpt-5.1-codex-mini"] != "gpt-5.1-codex-mini" {
		t.Fatalf("unexpected model mapping: %#v", credentials["model_mapping"])
	}
	quota, err := client.QueryQuota(context.Background(), settings, "secret", 101)
	if err != nil {
		t.Fatal(err)
	}
	window5H, window7D := quota.Windows()
	if window5H == nil || window5H.UsedPercent != 55.5 || window5H.LimitWindowSeconds != 18000 {
		t.Fatalf("unexpected 5-hour window: %+v", window5H)
	}
	if window7D == nil || window7D.UsedPercent != 100 || window7D.LimitWindowSeconds != 604800 {
		t.Fatalf("unexpected 7-day window: %+v", window7D)
	}
	if loginCalls.Load() != 1 {
		t.Fatalf("login calls = %d, want 1", loginCalls.Load())
	}
}

func TestApplyOAuthCredentialsKeepsExistingAccountID(t *testing.T) {
	var requestBody map[string]any
	var gotMethod string
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/login" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"admin-token"}}`))
			return
		}
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&requestBody)
		_, _ = w.Write([]byte(`{"code":0,"data":{"id":42,"name":"existing"}}`))
	}))
	defer ts.Close()

	account, err := New().ApplyOAuthCredentials(context.Background(), model.Sub2Settings{URL: ts.URL, Email: "admin@example.com"}, "secret", 42, map[string]any{
		"access_token": "new-at", "refresh_token": "new-rt", "chatgpt_account_id": "acct-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/admin/accounts/42/apply-oauth-credentials" {
		t.Fatalf("unexpected reauthorization request: %s %s", gotMethod, gotPath)
	}
	if account.ID != 42 || account.Name != "existing" {
		t.Fatalf("unexpected account response: %+v", account)
	}
	if requestBody["type"] != "oauth" {
		t.Fatalf("unexpected request type: %#v", requestBody["type"])
	}
	credentials, ok := requestBody["credentials"].(map[string]any)
	if !ok || credentials["access_token"] != "new-at" || credentials["refresh_token"] != "new-rt" {
		t.Fatalf("unexpected credentials payload: %#v", requestBody["credentials"])
	}
}

func TestRenameAccountUsesExistingAccountID(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/login" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"admin-token"}}`))
			return
		}
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"code":0,"data":{"id":42,"name":"renamed"}}`))
	}))
	defer ts.Close()

	account, err := New().RenameAccount(context.Background(), model.Sub2Settings{URL: ts.URL, Email: "admin@example.com"}, "secret", 42, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/admin/accounts/42" || account.ID != 42 || account.Name != "renamed" {
		t.Fatalf("unexpected rename result: path=%q account=%+v", gotPath, account)
	}
}

func TestRestoreSchedulingClearsTemporaryBlockAndVerifiesAccount(t *testing.T) {
	var calls []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/login" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"admin-token"}}`))
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/schedulable"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["schedulable"] != true {
				t.Errorf("schedulable body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":42}}`))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/temp-unschedulable"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"message":"cleared"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/accounts/42":
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":42,"name":"existing","status":"active","schedulable":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	account, err := New().RestoreScheduling(context.Background(), model.Sub2Settings{URL: ts.URL, Email: "admin@example.com"}, "secret", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !account.Schedulable || account.Status != "active" {
		t.Fatalf("account was not restored: %+v", account)
	}
	want := []string{
		"POST /api/v1/admin/accounts/42/schedulable",
		"DELETE /api/v1/admin/accounts/42/temp-unschedulable",
		"GET /api/v1/admin/accounts/42",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestQueryTotalCosts(t *testing.T) {
	for _, tt := range []struct {
		name        string
		summary     string
		standard    float64
		user        float64
		userPresent bool
		wantError   bool
	}{
		{"both", `{"total_standard_cost":12.3456,"total_cost":99,"total_user_cost":52.04}`, 12.3456, 52.04, true, false},
		{"zero", `{"total_standard_cost":0,"total_user_cost":0}`, 0, 0, true, false},
		{"missing_user", `{"total_standard_cost":4}`, 4, 0, false, false},
		{"null_user", `{"total_standard_cost":4,"total_user_cost":null}`, 4, 0, false, false},
		{"fallback", `{"total_cost":7,"total_user_cost":9}`, 7, 9, true, false},
		{"negative", `{"total_standard_cost":-2,"total_user_cost":-3}`, 0, 0, true, false},
		{"missing_standard", `{"total_user_cost":3}`, 0, 0, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var statsCalls atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/auth/login" {
					_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"admin-token"}}`))
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/accounts/42/stats" || r.URL.Query().Get("days") != "90" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
					return
				}
				statsCalls.Add(1)
				_, _ = w.Write([]byte(`{"code":0,"data":{"summary":` + tt.summary + `}}`))
			}))
			defer ts.Close()
			cost, err := New().QueryTotalCosts(context.Background(), model.Sub2Settings{URL: ts.URL, Email: "admin@example.com"}, "secret", 42)
			if (err != nil) != tt.wantError || cost.StandardCostUSD != tt.standard || (cost.UserCostUSD != nil) != tt.userPresent {
				t.Fatalf("cost = %+v, err=%v", cost, err)
			}
			if cost.UserCostUSD != nil && *cost.UserCostUSD != tt.user {
				t.Fatalf("user cost = %v, want %v", *cost.UserCostUSD, tt.user)
			}
			if statsCalls.Load() != 1 {
				t.Fatalf("stats requests = %d, want 1", statsCalls.Load())
			}
		})
	}
}
