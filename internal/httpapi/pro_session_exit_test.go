package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestProAutoExitObservationsNeverBlockPaymentOrTokenSave(t *testing.T) {
	for _, tc := range []struct{ name, baseline, actual string }{
		{"consistent", "198.51.100.8", "198.51.100.8"},
		{"changed", "198.51.100.8", "198.51.100.9"},
		{"missing_current", "198.51.100.8", ""},
		{"both_missing", "", ""},
		{"invalid_current", "198.51.100.8", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				fmt.Fprint(w, `{"code":0,"data":{"id":"fixture-order","status":"success"}}`)
			})
			_, err := st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.ProAuto = model.ProAutoState{ID: "fixture-session", Status: "running", Steps: map[string]string{"login": "running"}}
			})
			if err != nil {
				t.Fatal(err)
			}
			cfg, key, err := st.GPTPaySettings()
			if err != nil {
				t.Fatal(err)
			}
			card, secret, err := st.GPTPayCard("test-card")
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.proAutoRPC(context.Background(), email, cfg, key, card, secret, "pro_recharge", map[string]any{
				"exit_ip": tc.actual, "expected_exit_ip": tc.baseline, "client_id": "same-client",
				"session": map[string]any{"access_token": proAutoToken(email, "new-login"), "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}},
			})
			if err != nil {
				t.Fatalf("observation blocked recharge: %v", err)
			}
			_, err = s.proAutoRPC(context.Background(), email, cfg, key, card, secret, "pro_tokens", map[string]any{
				"exit_ip": tc.actual, "expected_exit_ip": tc.baseline, "client_id": "same-client",
				"tokens": map[string]any{"access_token": proAutoToken(email, "new-oauth"), "refresh_token": "new-rt"},
			})
			if err != nil {
				t.Fatalf("observation blocked token persistence: %v", err)
			}
			p, saved, err := st.MailAccountCredential(email)
			if err != nil || p.ProAuto.ExitIP != tc.baseline || p.ProAuto.Steps["oauth"] != "completed" || saved.AccessToken != proAutoToken(email, "new-oauth") || saved.RefreshToken != "new-rt" {
				t.Fatal("observation lost baseline or prevented new credentials")
			}
			_, orders, err := st.GPTPayOrders(10, 0, email)
			if err != nil || orders != 1 || requests.Load() != 1 {
				t.Fatal("observation caused duplicate or missing payment")
			}
		})
	}
}
