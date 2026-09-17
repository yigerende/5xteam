package httpapi

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

// Pro uses the existing mailbox-only entry to the same executeCodexOAuth
// engine as Team step 3. Test the persistence/status boundary without logging
// into real accounts or triggering a Team workflow.
func TestProOAuthLoginCompletionAndProgress(t *testing.T) {
	for _, scenario := range []string{"success", "missing_rt", "failure", "dead"} {
		t.Run(scenario, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			const email = "pro-oauth@example.com"
			_, err = st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "old-at", RefreshToken: "old-rt", ChatGPTSession: `{"accessToken":"web-at"}`})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.UpdateMailAccountManagementScope(email, "pro"); err != nil {
				t.Fatal(err)
			}
			_, err = st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.PushProvider, p.PushStatus, p.Sub2AccountID, p.CPAAuthFileName = "sub2", "completed", 123, "keep.json"
				p.InviteStatus, p.AcceptStatus, p.TransferStatus, p.RemoveStatus = "completed", "completed", "completed", "completed"
				p.SpaceMergedOnce = true
			})
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{store: st, oauthJobs: map[string]map[string]any{"pro-job": {"job_id": "pro-job", "email": email, "status": "queued", "logs": []any{}}}}
			messages := []string{"OAuth 登录方式：邮箱验证码", "邮箱验证码提交后验证 2FA", "OAuth 回调换取 AT / RT"}
			for _, message := range messages {
				s.updateOAuthJob("pro-job", "running", message)
			}
			checkJob := func(want string) {
				t.Helper()
				r := httptest.NewRequest("GET", "/api/mail/oauth/pro-job", nil)
				r.SetPathValue("job_id", "pro-job")
				out := httptest.NewRecorder()
				s.freeAccountOAuthStatus(out, r)
				var body struct {
					Data struct {
						Job struct {
							Status string `json:"status"`
							Logs   []struct {
								Message string `json:"message"`
							} `json:"logs"`
						} `json:"job"`
					} `json:"data"`
				}
				if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &body) != nil {
					t.Fatal("status endpoint failed")
				}
				if body.Data.Job.Status != want || len(body.Data.Job.Logs) != len(messages) {
					t.Fatalf("status/progress mismatch: %+v", body.Data.Job)
				}
				for i, log := range body.Data.Job.Logs {
					if log.Message != messages[i] {
						t.Fatal("progress order changed")
					}
				}
			}
			checkJob("running")
			result := map[string]any{"success": true, "access_token": "new-at", "refresh_token": "new-rt"}
			var runErr error
			switch scenario {
			case "missing_rt":
				delete(result, "refresh_token")
			case "failure":
				runErr = errors.New("fixture timeout")
			case "dead":
				result = map[string]any{"success": false, "dead": true, "error_code": "account_deactivated", "error": "account_deactivated"}
			}
			s.finishMailAccountOAuthJob("pro-job", email, result, runErr)
			p, credentials, err := st.MailAccountCredential(email)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "success" {
				checkJob("success")
				if credentials.AccessToken != "new-at" || credentials.RefreshToken != "new-rt" || p.OAuthStatus != "completed" {
					t.Fatal("new OAuth tokens not persisted")
				}
			} else {
				checkJob("failed")
				if credentials.AccessToken != "old-at" || credentials.RefreshToken != "old-rt" {
					t.Fatal("failed login overwrote credentials")
				}
			}
			if scenario == "dead" && p.ChatGPTStatus != "dead" {
				t.Fatal("dead result not marked")
			}
			if p.ManagementScope != "pro" || p.PushStatus != "completed" || p.Sub2AccountID != 123 || p.CPAAuthFileName != "keep.json" || !p.SpaceMergedOnce || p.RemoveStatus != "completed" {
				t.Fatal("OAuth modified unrelated Pro state")
			}
			if credentials.ChatGPTSession != `{"accessToken":"web-at"}` {
				t.Fatal("Codex OAuth must not replace web Session")
			}
			if len(st.FreeAccounts()) != 0 {
				t.Fatal("Pro login created a Team rotation record")
			}
		})
	}
}
