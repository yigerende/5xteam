package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestSelectOAuthLoginCredentialMatrix(t *testing.T) {
	for _, mode := range []string{"", "email_otp", "password_totp"} {
		for _, password := range []string{"", " \t", "test-password"} {
			for _, secret := range []string{"", " \t", "JBSWY3DPEHPK3PXP"} {
				t.Run(fmt.Sprintf("%s/password=%t/totp=%t", mode, strings.TrimSpace(password) != "", strings.TrimSpace(secret) != ""), func(t *testing.T) {
					login := selectOAuthLogin(mode, model.MailAccountCredentials{GptPassword: password, TotpSecret: secret})
					want := "email_otp"
					if mode == "password_totp" && strings.TrimSpace(password) != "" && strings.TrimSpace(secret) != "" {
						want = "password_totp"
					}
					if login.selected != want {
						t.Fatalf("selected %s, want %s", login.selected, want)
					}
					if (login.fallback != "" && login.reason != "") != (mode == "password_totp" && want == "email_otp") {
						t.Fatal("missing or unexpected fallback explanation")
					}
					payload := map[string]any{}
					login.apply(payload)
					if payload["login_mode"] != want {
						t.Fatal("child process mode differs from selection")
					}
					if want == "email_otp" && (payload["gpt_password"] != "" || payload["totp_secret"] != "") {
						t.Fatal("email mode passed authentication credentials to password/TOTP path")
					}
					if want == "password_totp" && (payload["gpt_password"] != password || payload["totp_secret"] != secret) {
						t.Fatal("2FA mode did not pass existing credentials unchanged")
					}
					encoded, _ := json.Marshal(login.details())
					if strings.Contains(string(encoded), "test-password") || strings.Contains(string(encoded), "JBSWY3DPEHPK3PXP") {
						t.Fatal("login metadata contains credentials")
					}
				})
			}
		}
	}
}

func TestOAuthLoginSelectionIsolatedAcrossAccountsAndRetries(t *testing.T) {
	var workers sync.WaitGroup
	for i := range 30 {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			credentials := model.MailAccountCredentials{Email: fmt.Sprintf("%d@example.com", index), GptPassword: fmt.Sprintf("password-%d", index), TotpSecret: "secret"}
			login := selectOAuthLogin("password_totp", credentials)
			credentials.GptPassword = "changed"
			for range 3 {
				payload := map[string]any{}
				login.apply(payload)
				if payload["gpt_password"] != fmt.Sprintf("password-%d", index) {
					t.Error("credential snapshot changed or leaked from another job")
				}
				payload["gpt_password"] = "mutated-child-input"
			}
		}(i)
	}
	workers.Wait()
}

func TestOAuthLoginModeSnapshotAndEarlyFailureDiagnostics(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// No proxy: stop before networking, after login selection.
	if err := st.SaveSettings(model.DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: "mode@example.com"}, model.MailAccountCredentials{Email: "mode@example.com", GptPassword: "password", TotpSecret: "JBSWY3DPEHPK3PXP"}); err != nil {
		t.Fatal(err)
	}
	settings := model.DefaultAutoRotationSettings()
	settings.OAuthLoginMode = "password_totp"
	if _, err := st.SaveAutoRotationSettings(settings); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: st}
	var events []protocolOAuthDiagnostic
	result, runErr := server.executeCodexOAuth("mode@example.com", func(string) {
		settings.OAuthLoginMode = "email_otp"
		if _, err := st.SaveAutoRotationSettings(settings); err != nil {
			t.Fatal(err)
		}
	}, func(event protocolOAuthDiagnostic) { events = append(events, event) })
	if runErr == nil {
		t.Fatal("missing proxy should fail before networking")
	}
	if result["configured_login_mode"] != "password_totp" || result["selected_login_mode"] != "password_totp" || result["login_method"] != "not_started" {
		t.Fatalf("in-flight mode changed or login falsely reported: %+v", result)
	}
	if len(events) < 2 || events[0].Event != "login_selected" || events[len(events)-1].Event != "login_result" {
		t.Fatalf("early failure lost selection/result events: %+v", events)
	}
	if result["success"] != false || events[len(events)-1].Details["oauth_success"] != false {
		t.Fatal("early failure reported success")
	}
	result, _ = server.executeCodexOAuth("mode@example.com", nil, nil)
	if result["configured_login_mode"] != "email_otp" {
		t.Fatal("new job must use new settings")
	}
}
