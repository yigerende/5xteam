package sub2

import (
	"chatgpt-space-merge/internal/model"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSIWCMarkerCreateAndReauthorization(t *testing.T) {
	for _, siwc := range []bool{false, true} {
		t.Run(fmt.Sprint(siwc), func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/login" {
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"test"}}`)
					return
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				extra, _ := body["extra"].(map[string]any)
				if siwc && (extra["auth_provider"] != "siwc" || extra["import_source"] != "siwc" || extra["codex_fingerprint_mode"] != "off") {
					t.Error("SIWC marker missing")
				}
				if !siwc && extra != nil {
					t.Error("ordinary OAuth was changed")
				}
				json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"id": 42, "extra": extra}})
			}))
			defer remote.Close()
			client := New()
			cfg := model.Sub2Settings{URL: remote.URL, Email: "admin@test.invalid"}
			creds := map[string]any{"access_token": "test"}
			if siwc {
				creds["auth_mode"] = "siwc"
			}
			if _, e := client.CreateAccount(context.Background(), cfg, "test", CreateAccountInput{Credentials: creds, GroupIDs: []int64{8}}, "marker"); e != nil {
				t.Fatal(e)
			}
			if _, e := client.ApplyOAuthCredentials(context.Background(), cfg, "test", 42, creds); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestSIWCCallbackImportContract(t *testing.T) {
	proxy := int64(9)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"test"}}`)
			return
		}
		if r.URL.Path != "/api/v1/admin/openai/siwc/integration/callback" {
			t.Fatal("generic create must not run")
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if r.Header.Get("Idempotency-Key") != "same-import" || b["proxy_id"] != float64(9) || b["web_access_token"] != "web" || len(b["group_ids"].([]any)) != 2 {
			t.Fatal("missing import configuration")
		}
		tokens := model.SIWCCredentials{AuthMode: "siwc", Email: "user@test.invalid", ExtAgentHostID: "host", AccessToken: "at", RefreshToken: "rt", IDToken: "id", Subject: "subject", ClientID: "client", PlanUseEnabled: true, ExpiresAt: time.Now().Add(time.Hour).Unix()}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"account": map[string]any{"id": 42, "extra": map[string]any{"auth_provider": "siwc"}}, "tokens": tokens}})
	}))
	defer remote.Close()
	a, e := New().CreateAccount(context.Background(), model.Sub2Settings{URL: remote.URL, Email: "admin@test.invalid"}, "test", CreateAccountInput{ProxyID: &proxy, GroupIDs: []int64{2, 3}, Concurrency: 20, Priority: 4, SIWCImport: &SIWCImportInput{SessionID: "session", CallbackURL: "http://127.0.0.1:1456/auth/callback?code=private", Email: "user@test.invalid", HostID: "host", WebAccessToken: "web", WebAccountID: "personal"}}, "same-import")
	if e != nil {
		t.Fatal(e)
	}
	if a.ID != 42 || a.SIWCTokens.WebAccessToken != "web" {
		t.Fatal("import result lost")
	}
}
