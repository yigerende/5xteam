package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDisplayReadsUseIndependentConnection(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("display contacted upstream") })
	// Saved records remain readable when the main application's single SQL
	// connection is unavailable. Real writes/legacy OAuth repair use the writer.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/api/pro-settings", s.getProSettings}, {"/api/mail/accounts?page=1&page_size=10", s.listMailAccounts},
	} {
		w := httptest.NewRecorder()
		tc.handler(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != 200 || strings.Contains(w.Body.String(), "database is closed") {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	page := s.parsePagination(httptest.NewRequest("GET", "/api/items?page=2&page_size=100", nil))
	if page.PageSize != 100 || page.Offset != 100 {
		t.Fatal(page)
	}
}
