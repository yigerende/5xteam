package sub2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		CpaWS: true,
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
