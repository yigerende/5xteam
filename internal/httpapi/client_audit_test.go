package httpapi

import (
	"chatgpt-space-merge/internal/clientaudit"
	"chatgpt-space-merge/internal/store"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientAuditHTTPIsolation(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, sessions: map[string]time.Time{"audit": time.Now().Add(time.Hour)}}
	db, err := clientaudit.OpenDatabase(st.ClientAuditPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// No application monitor is started by this test; fixture has no source server.
	s.clientAudit = clientaudit.NewEngine(db, clientaudit.Postgres{}, st.ClientAuditSettings)
	mux := http.NewServeMux()
	s.registerClientAudit(mux)
	h := s.authMiddleware(mux)
	call := func(method, path, body, origin string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: "audit"})
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	for _, path := range []string{"settings", "status", "report", "detail", "progress", "export"} {
		if w := call("GET", "/api/client-audit/"+path, "", "", false); w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	if w := call("PUT", "/api/client-audit/settings", "{}", "https://elsewhere.invalid", true); w.Code != 403 {
		t.Fatal(w.Code)
	}
	c := clientaudit.DefaultConfig()
	c.DBHost = "127.0.0.1"
	c.DBName = "fixture"
	c.DBUser = "read"
	c.Enabled = true
	body, _ := json.Marshal(auditSettingsInput{Config: c, DBPassword: "test-only-secret"})
	w := call("PUT", "/api/client-audit/settings", string(body), "", true)
	if w.Code != 200 || strings.Contains(w.Body.String(), "test-only-secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	v, secret, err := st.ClientAuditSettings()
	if err != nil || secret.DBPassword != "test-only-secret" || !v.DBPasswordPresent {
		t.Fatal(v, err)
	}
	w = call("GET", "/api/client-audit/status", "", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"last_attempt":0`) {
		t.Fatal("saving initiated a source read", w.Body.String())
	}
	for _, path := range []string{"report?mode=day", "progress", "detail?mode=day&user_id=1", "export?mode=day"} {
		w = call("GET", "/api/client-audit/"+path, "", "", true)
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	w = call("GET", "/api/client-audit/report?mode=invalid", "", "", true)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	body, _ = json.Marshal(auditSettingsInput{Config: c})
	w = call("PUT", "/api/client-audit/settings", string(body), "", true)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	_, secret, _ = st.ClientAuditSettings()
	if secret.DBPassword != "test-only-secret" {
		t.Fatal("blank password erased saved secret")
	}
}
