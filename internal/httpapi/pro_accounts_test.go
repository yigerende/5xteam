package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestParseProOAuthCallback(t *testing.T) {
	code, state, err := parseProOAuthCallback("http://localhost:1455/auth/callback?code=abc%2B123&state=xyz", "", "")
	if err != nil || code != "abc+123" || state != "xyz" {
		t.Fatalf("unexpected result code=%q state=%q err=%v", code, state, err)
	}
	if _, _, err = parseProOAuthCallback("http://localhost:1455/auth/callback?error=access_denied", "", ""); err == nil {
		t.Fatal("expected OAuth callback error")
	}
}

func TestBuildProCredentialsAlwaysUsesProPlan(t *testing.T) {
	payload := buildProCredentials(model.MailAccountProfile{Email: "pro@example.com", OAuthAccountID: "acc"}, model.MailAccountCredentials{AccessToken: "at", RefreshToken: "rt", IDToken: "id"})
	if payload["plan_type"] != "pro" || payload["chatgpt_plan_type"] != "pro" || payload["id_token"] != "id" {
		t.Fatalf("unexpected credentials: %#v", payload)
	}
}

func TestRetryProStepRetriesAndStops(t *testing.T) {
	attempts := 0
	err := retryProStep(t.Context(), 2, 1, func() error {
		attempts++
		if attempts < 3 {
			return url.InvalidHostError("temporary")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestProManagementRequiresATAndSuppliesSelectedTokens(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	for _, item := range []struct {
		email string
		token string
	}{
		{email: "ready-pro@example.com", token: "ready-access-token"},
		{email: "missing-at@example.com"},
	} {
		if _, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: item.email}, model.MailAccountCredentials{
			Email: item.email, PickupURL: "https://mail.example/pickup", AccessToken: item.token,
		}); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{store: dataStore}

	missingRequest := httptest.NewRequest(http.MethodPut, "/api/mail/accounts/missing-at@example.com/management-scope", strings.NewReader(`{"scope":"pro"}`))
	missingRequest.SetPathValue("email", "missing-at@example.com")
	missingResponse := httptest.NewRecorder()
	server.updateMailAccountManagementScope(missingResponse, missingRequest)
	if missingResponse.Code != http.StatusConflict || !strings.Contains(missingResponse.Body.String(), "尚未保存 AT") {
		t.Fatalf("missing AT response: status=%d body=%s", missingResponse.Code, missingResponse.Body.String())
	}

	moveRequest := httptest.NewRequest(http.MethodPut, "/api/mail/accounts/ready-pro@example.com/management-scope", strings.NewReader(`{"scope":"pro"}`))
	moveRequest.SetPathValue("email", "ready-pro@example.com")
	moveResponse := httptest.NewRecorder()
	server.updateMailAccountManagementScope(moveResponse, moveRequest)
	if moveResponse.Code != http.StatusOK {
		t.Fatalf("move response: status=%d body=%s", moveResponse.Code, moveResponse.Body.String())
	}

	mailResponse := httptest.NewRecorder()
	server.listMailAccounts(mailResponse, httptest.NewRequest(http.MethodGet, "/api/mail/accounts", nil))
	if strings.Contains(mailResponse.Body.String(), "ready-pro@example.com") || !strings.Contains(mailResponse.Body.String(), "missing-at@example.com") {
		t.Fatalf("mail list scope mismatch: %s", mailResponse.Body.String())
	}
	proResponse := httptest.NewRecorder()
	server.listProAccounts(proResponse, httptest.NewRequest(http.MethodGet, "/api/pro-accounts", nil))
	if proResponse.Code != http.StatusOK || !strings.Contains(proResponse.Body.String(), "ready-pro@example.com") || strings.Contains(proResponse.Body.String(), "ready-access-token") {
		t.Fatalf("Pro list response leaked or omitted data: status=%d body=%s", proResponse.Code, proResponse.Body.String())
	}

	tokenRequest := httptest.NewRequest(http.MethodPost, "/api/pro-accounts/access-tokens", strings.NewReader(`{"emails":["ready-pro@example.com"]}`))
	tokenResponse := httptest.NewRecorder()
	server.proAccountAccessTokens(tokenResponse, tokenRequest)
	if tokenResponse.Code != http.StatusOK || !strings.Contains(tokenResponse.Body.String(), "ready-access-token") {
		t.Fatalf("selected token response: status=%d body=%s", tokenResponse.Code, tokenResponse.Body.String())
	}
}

func TestProPlanCheckRequiresGlobalProxy(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	const email = "pro-plan@example.com"
	if _, err = dataStore.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "saved-at"}); err != nil {
		t.Fatal(err)
	}
	if _, err = dataStore.UpdateMailAccountManagementScope(email, "pro"); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: dataStore}
	request := httptest.NewRequest(http.MethodPost, "/api/pro-accounts/pro-plan@example.com/check-plan", nil)
	request.SetPathValue("email", email)
	response := httptest.NewRecorder()
	server.checkProAccountPlan(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "全局代理") {
		t.Fatalf("unexpected no-proxy response: status=%d body=%s", response.Code, response.Body.String())
	}
}
