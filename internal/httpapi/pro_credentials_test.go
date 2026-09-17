package httpapi

import (
	"encoding/base64"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestProCredentialsUseExistingMailViewAndExport(t *testing.T) {
	for _, missingRT := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "missing_rt"}[missingRT], func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			email := "pro+fixture@example.com"
			payload := `{"https://api.openai.com/profile":{"email":"pro+fixture@example.com"},"https://api.openai.com/auth":{"chatgpt_account_id":"pro-team","chatgpt_user_id":"pro-user","chatgpt_plan_type":"pro"}}`
			at := "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
			rt := "fixture-pro-rt"
			if missingRT {
				rt = ""
			}
			_, err = st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: at, RefreshToken: rt, GptPassword: "fixture-password", TotpSecret: "JBSWY3DPEHPK3PXP"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.UpdateMailAccountManagementScope(email, "pro"); err != nil {
				t.Fatal(err)
			}
			before, credsBefore, err := st.MailAccountCredential(email)
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{store: st}
			for _, format := range []string{"raw", "cpa", "sub2"} {
				req := httptest.NewRequest("GET", "/credentials?format="+format, nil)
				req.SetPathValue("email", email)
				out := httptest.NewRecorder()
				s.exportMailAccountCredentials(out, req)
				if missingRT && format != "raw" {
					if out.Code != 409 {
						t.Fatalf("missing RT must block export: %s", out.Body.String())
					}
					continue
				}
				if out.Code != 200 || out.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("%s failed: %d %s", format, out.Code, out.Body.String())
				}
				if !strings.Contains(out.Body.String(), at) || (!missingRT && !strings.Contains(out.Body.String(), rt)) {
					t.Fatal("stored Pro tokens missing")
				}
				if format == "raw" {
					if !strings.Contains(out.Body.String(), "JBSWY3DPEHPK3PXP") || !strings.Contains(out.Body.String(), "fixture-password") {
						t.Fatal("missing stored account credentials")
					}
				} else if !strings.Contains(out.Body.String(), `"plan_type":"pro"`) {
					t.Fatalf("wrong Pro export plan: %s", out.Body.String())
				}
			}
			after, credsAfter, err := st.MailAccountCredential(email)
			if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(credsBefore, credsAfter) {
				t.Fatal("view/export changed Pro account or tokens")
			}
		})
	}
}
