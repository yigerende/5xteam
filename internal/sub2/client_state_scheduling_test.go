package sub2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestReauthorizationPreservesStateRecoveryPause(t *testing.T) {
	for _, tc := range []struct {
		name       string
		account    string
		wantError  bool
		wantFollow bool
	}{
		{"pending-state", `{"id":42,"status":"active","schedulable":false,"extra":{"state_scheduling_pending":true}}`, false, false},
		{"recovery-queued", `{"id":42,"status":"active","schedulable":false,"extra":{"quality_schedulable_restore":true}}`, false, false},
		{"manual-pause", `{"id":42,"status":"active","schedulable":false,"extra":{"state_scheduling_manual":true}}`, false, false},
		{"account-still-error", `{"id":42,"status":"error","schedulable":false,"extra":{"state_scheduling_pending":true}}`, true, false},
		{"ordinary-401-recovery", `{"id":42,"status":"active","schedulable":false,"extra":{"state_scheduling_pending":false,"quality_schedulable_restore":false}}`, false, true},
		{"legacy-sub", `{"id":42,"status":"active","schedulable":false}`, false, true},
		{"failed-recovery", `{"id":42,"status":"error","schedulable":false}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var applied, enableCalls, clearCalls, getCalls int
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/v1/auth/login":
					_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"admin-token"}}`))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/apply-oauth-credentials"):
					applied++
					_, _ = w.Write([]byte(`{"code":0,"data":` + tc.account + `}`))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/schedulable"):
					enableCalls++
					_, _ = w.Write([]byte(`{"code":0,"data":{"id":42}}`))
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/temp-unschedulable"):
					clearCalls++
					_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/accounts/42":
					getCalls++
					status := "active"
					if tc.name == "failed-recovery" {
						status = "error"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": Account{ID: 42, Status: status, Schedulable: status == "active"}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			client := New()
			settings := model.Sub2Settings{URL: ts.URL, Email: "admin@example.com"}
			reauthorized, err := client.ApplyOAuthCredentials(t.Context(), settings, "secret", 42, map[string]any{"access_token": "reauthorized"})
			if err != nil {
				t.Fatal(err)
			}
			account, err := client.RestoreScheduling(t.Context(), settings, "secret", reauthorized)
			if (err != nil) != tc.wantError {
				t.Fatalf("account=%+v err=%v wantError=%t", account, err, tc.wantError)
			}
			wantCalls := 0
			if tc.wantFollow {
				wantCalls = 1
			}
			if applied != 1 || enableCalls != wantCalls || clearCalls != wantCalls || getCalls != wantCalls {
				t.Fatalf("unexpected calls: applied=%d enable=%d clear=%d get=%d", applied, enableCalls, clearCalls, getCalls)
			}
			if !tc.wantError && !tc.wantFollow && (account.ID != 42 || account.Schedulable) {
				t.Fatalf("State recovery must keep the original account paused: %+v", account)
			}
		})
	}
}
