package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

func TestLoginProtectsAPIAndDefaultAdminCanLogin(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	api, err := New(dataStore, workflow.NewManager(dataStore))
	if err != nil {
		t.Fatal(err)
	}
	handler := api.Handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"admin"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	if cookie.Name != authCookieName || cookie.Value == "" {
		t.Fatalf("missing session cookie: %+v", cookie)
	}

	authorized := httptest.NewRecorder()
	settingsReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	settingsReq.AddCookie(cookie)
	handler.ServeHTTP(authorized, settingsReq)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d", authorized.Code)
	}
}
