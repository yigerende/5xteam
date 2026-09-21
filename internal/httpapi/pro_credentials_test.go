package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestProInitialCredentialsSavedWhenCardDisabledAfterLogin(t *testing.T) {
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("disabled card submitted a payment") })
	configureProAutoFixture(t, s)
	s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
		card, secret, err := st.GPTPayCard("test-card")
		if err != nil {
			return err
		}
		card.Enabled = false
		if _, err = st.SaveGPTPayCard(card, secret); err != nil {
			return err
		}
		_, err = rpc("pro_recharge", map[string]any{"session": map[string]any{"access_token": proAutoToken(email, "first"), "session_token": "saved-cookie", "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}}})
		return err
	}, Push: func(context.Context, string) (model.MailAccountProfile, error) {
		t.Error("should not push")
		return model.MailAccountProfile{}, nil
	}}
	out := payCall(t, s.startProAutomation, "email", email, map[string]any{"card_id": "test-card", "plan_code": "pro20"})
	if out.Code != 202 {
		t.Fatal(out.Body.String())
	}
	p := waitProAutoFinished(t, s, email)
	_, c, err := st.MailAccountCredential(email)
	if err != nil || c.AccessToken != proAutoToken(email, "first") || !strings.Contains(c.ChatGPTSession, "saved-cookie") {
		t.Fatal("successful login lost when card disabled")
	}
	if p.ProAuto.Steps["login"] != "completed" || p.ProAuto.Steps["recharge"] != "failed" || p.ProAuto.OrderID != "" {
		t.Fatal("incorrect login/payment checkpoint")
	}
}
