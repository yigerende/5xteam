package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestGenerateOpenAIPKCEAuthorizationMatchesCodexFlow(t *testing.T) {
	auth, err := GenerateOpenAIPKCEAuthorization()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(auth.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	want := map[string]string{"client_id": openAIClientID, "redirect_uri": OpenAIOAuthRedirectURI, "response_type": "code", "scope": "openid profile email offline_access", "code_challenge_method": "S256", "id_token_add_organizations": "true", "codex_cli_simplified_flow": "true"}
	for key, value := range want {
		if query.Get(key) != value {
			t.Fatalf("%s=%q want %q", key, query.Get(key), value)
		}
	}
	if query.Get("state") != auth.State || auth.SessionID == "" || len(auth.CodeVerifier) != 128 || query.Get("code_challenge") == "" {
		t.Fatalf("invalid PKCE bundle: %+v", auth)
	}
}

func TestExchangeOpenAIOAuthCodeFormAndTokenSet(t *testing.T) {
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(body))
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "refresh_token": "rt", "id_token": "id", "expires_in": 3600})
	}))
	defer server.Close()
	tokens, err := exchangeOpenAIOAuthCodeAt(context.Background(), server.URL, "code-value", "verifier-value", model.Settings{RequestTimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != openAIClientID || form.Get("redirect_uri") != OpenAIOAuthRedirectURI || form.Get("code_verifier") != "verifier-value" {
		t.Fatalf("unexpected form: %v", form)
	}
	if tokens.AccessToken != "at" || tokens.RefreshToken != "rt" || tokens.IDToken != "id" || time.Until(tokens.ExpiresAt) < 59*time.Minute {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}
}

func TestExchangeOpenAIOAuthCodeRequiresGlobalProxy(t *testing.T) {
	_, err := ExchangeOpenAIOAuthCode(context.Background(), "code", strings.Repeat("v", 43), model.Settings{})
	if err == nil || !strings.Contains(err.Error(), "全局代理") {
		t.Fatalf("expected global proxy error, got %v", err)
	}
}

func TestOAuthTokenRetryClassification(t *testing.T) {
	for _, err := range []error{
		errors.New("curl: (5) Could not resolve proxy"),
		errors.New("OAuth 刷新失败（HTTP 503）: unavailable"),
		errors.New("proxy connect aborted"),
	} {
		if !isRetryableOAuthTokenError(err) {
			t.Fatalf("expected retryable token error: %v", err)
		}
	}
	if isRetryableOAuthTokenError(nonRetryableOAuthError{errors.New("OAuth 授权码交换失败（HTTP 400）: invalid_grant")}) {
		t.Fatal("business token error must not be retryable")
	}
}
