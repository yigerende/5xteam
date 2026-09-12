package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestMailTOTPMatchesPyOTPDefaults(t *testing.T) {
	// RFC 6238 SHA-1 vectors reduced to pyotp's default six digits.
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, tc := range []struct {
		unix int64
		code string
	}{
		{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"},
		{1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"},
	} {
		for _, key := range []string{secret, strings.ToLower(secret), " " + secret + " "} {
			result, err := generateMailTOTP(key, time.Unix(tc.unix, 0))
			if err != nil || result.Code != tc.code {
				t.Fatalf("TOTP mismatch at %d: %v", tc.unix, err)
			}
			if result.ValidForMS != (30-tc.unix%30)*1000 || result.ExpiresAt.Unix() != (tc.unix/30+1)*30 {
				t.Fatal("incorrect validity period")
			}
		}
	}
	before, err := generateMailTOTP(secret, time.Unix(59, 999000000))
	if err != nil || before.ValidForMS != 1 {
		t.Fatal("end-of-period validity", err)
	}
	after, err := generateMailTOTP(secret, time.Unix(60, 0))
	if err != nil || after.ValidForMS != 30000 || before.Code == after.Code {
		t.Fatal("new-period validity", err)
	}
	for _, invalid := range []string{"", "  ", "invalid-2fa-key!"} {
		result, err := generateMailTOTP(invalid, time.Now())
		if err == nil || result.Code != "" || strings.Contains(err.Error(), "invalid-2fa-key") {
			t.Fatal("invalid key did not fail safely")
		}
	}
}

func TestMailTOTPReadsOnlySelectedAccountWithoutAT(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, tc := range []struct {
		email, key string
		status     int
	}{
		{"totp@example.com", secret, 200},
		{"unset@example.com", "", 409},
		{"invalid@example.com", "invalid-2fa-key!", 409},
		{"missing@example.com", "", 404},
	} {
		var before model.MailAccountProfile
		if tc.status != 404 {
			before, err = st.SaveMailAccount(model.MailAccountProfile{Email: tc.email}, model.MailAccountCredentials{Email: tc.email, TotpSecret: tc.key})
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest("GET", "/totp", nil)
		r.SetPathValue("email", tc.email)
		w := httptest.NewRecorder()
		(&Server{store: st}).getMailAccountTOTP(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: status=%d", tc.email, w.Code)
		}
		if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), "invalid-2fa-key") || strings.Contains(w.Body.String(), "totp_secret") {
			t.Fatal("TOTP response included the secret")
		}
		if tc.status == 200 {
			var result struct{ Data mailTOTPCode }
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !regexp.MustCompile(`^\d{6}$`).MatchString(result.Data.Code) || result.Data.ValidForMS < 0 || result.Data.ValidForMS > 30000 {
				t.Fatal("invalid TOTP response")
			}
		}
		if tc.status != 404 {
			after, creds, err := st.MailAccountCredential(tc.email)
			if err != nil || !before.UpdatedAt.Equal(after.UpdatedAt) || creds.AccessToken != "" {
				t.Fatal("TOTP generation mutated account or required login")
			}
		}
	}
}
