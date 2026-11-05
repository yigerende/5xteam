package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestSelectUnloggedProAccountsAPI(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, item := range []struct{ email, token string }{{"new@example.com", ""}, {"logged@example.com", "secret-at"}} {
		_, err := s.SaveMailAccount(model.MailAccountProfile{Email: item.email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: item.email, AccessToken: item.token, GptPassword: "secret-password"})
		if err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{store: s}
	response := httptest.NewRecorder()
	server.selectUnloggedProAccounts(response, httptest.NewRequest(http.MethodGet, "/api/pro-accounts/select-unlogged?query=NEW", nil))
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var emails []string
	if err := json.Unmarshal(body.Data["emails"], &emails); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || !reflect.DeepEqual(emails, []string{"new@example.com"}) {
		t.Fatal(response.Body.String())
	}
	anonymous := httptest.NewRecorder()
	server.authMiddleware(http.HandlerFunc(server.selectUnloggedProAccounts)).ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/pro-accounts/select-unlogged", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous selection status = %d", anonymous.Code)
	}
}
