package httpapi

import (
	"chatgpt-space-merge/internal/ejh"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEJHSigningEndpointPublicOnlyAndCSRF(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("key generation must not contact issuer") })
	initial := ejh.DefaultSettings()
	initial.AppKey = "fixture"
	if _, err := st.SaveEJHSettings(initial, "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	request := func(method, path, body, origin string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if auth {
			r.AddCookie(&http.Cookie{Name: authCookieName, Value: s.createSession()})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "/api/ejh/signing-key", "{}", "", false); w.Code != 401 {
		t.Fatal("anonymous generation", w.Code)
	}
	if w := request("POST", "/api/ejh/signing-key", "{}", "https://attacker.invalid", true); w.Code != 403 {
		t.Fatal("CSRF generation", w.Code)
	}
	s.ejh.mu.Lock()
	w := request("POST", "/api/ejh/signing-key", "{}", "http://example.com", true)
	s.ejh.mu.Unlock()
	if w.Code != 409 {
		t.Fatal("generated during operation", w.Code)
	}
	w = request("POST", "/api/ejh/signing-key", "{}", "http://example.com", true)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("generation failed", w.Code)
	}
	cfg, _, err := s.store.EJHSettings()
	if err != nil || !cfg.SigningKeyPresent {
		t.Fatal("key not saved", err)
	}
	for _, response := range []*httptest.ResponseRecorder{w, request("GET", "/api/ejh/settings", "", "", true), request("POST", "/api/ejh/signing-key", "{}", "http://example.com", true)} {
		if response.Code != 200 || !strings.Contains(response.Body.String(), cfg.SigningPublicKey) || strings.Contains(response.Body.String(), cfg.SigningPrivateKey) || strings.Contains(response.Body.String(), "private_key") || strings.Contains(response.Body.String(), "fixture-secret") {
			t.Fatal("public response incorrect", response.Code)
		}
		var decoded map[string]any
		if json.Unmarshal(response.Body.Bytes(), &decoded) != nil {
			t.Fatal("invalid JSON")
		}
	}
	w = request("POST", "/api/ejh/signing-key", `{"regenerate":true}`, "http://example.com", true)
	rotated, _, err := st.EJHSettings()
	if err != nil || w.Code != 200 || rotated.SigningPublicKey == cfg.SigningPublicKey || strings.Contains(w.Body.String(), rotated.SigningPrivateKey) {
		t.Fatal("rotation endpoint failed", w.Code, err)
	}
}
