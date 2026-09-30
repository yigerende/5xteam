package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"chatgpt-space-merge/internal/gptpay"
)

func TestGPTPayCatalogReadsDraftURLWithoutSavingOrSendingKey(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("catalog used old supplier") })
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/catalog" || r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
			t.Error("catalog leaked key or incorrect path")
		}
		fmt.Fprint(w, `{"code":0,"data":{"countries":[{"code":"PH","name":"菲律宾","currency":"PHP"}],"creditPrices":{"pro50x":100}}}`)
	}))
	defer server.Close()
	query := "/api/gptpay/catalog?provider=cmsnav&url=" + url.QueryEscape(server.URL)
	rec := httptest.NewRecorder()
	s.getGPTPayCatalog(rec, httptest.NewRequest("GET", query, nil))
	var env struct {
		Data gptpay.Catalog `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != 200 || len(env.Data.Countries) != 1 || calls != 1 {
		t.Fatalf("catalog route: %s", rec.Body.String())
	}
	cfg, key, err := st.GPTPaySettings()
	if err != nil || cfg.Provider != gptpay.Tokenseek || key != "test-api-key" {
		t.Fatal("reading catalog changed saved config")
	}
	for _, q := range []string{"provider=tokenseek", "provider=cmsnav&url=http://example.com", "provider=cmsnav&url=https://user:password@example.com"} {
		rec = httptest.NewRecorder()
		s.getGPTPayCatalog(rec, httptest.NewRequest("GET", "/api/gptpay/catalog?"+q, nil))
		if rec.Code != 400 || calls != 1 {
			t.Fatal("invalid query was accepted")
		}
	}
	if _, err = st.SaveGPTPaySettings(gptpay.Settings{Provider: gptpay.CMSNav, URL: server.URL}, ""); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.getGPTPayCatalog(rec, httptest.NewRequest("GET", "/api/gptpay/catalog?provider=cmsnav", nil))
	if rec.Code != 200 || calls != 2 {
		t.Fatal("saved supplier catalog should work without key")
	}
}
