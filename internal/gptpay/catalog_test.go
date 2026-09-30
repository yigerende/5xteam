package gptpay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestCMSNavCatalogPublicProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/prefix/api/catalog" || r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" || r.URL.RawQuery != "" {
			t.Error("wrong public catalog request")
		}
		fmt.Fprint(w, `{"code":0,"data":{"countries":[{"code":"US","name":"美国","currency":"USD"},{"code":"PH","name":"菲律宾"}],"currencies":{"PH":"PHP"},"creditPrices":{"pro5x":80,"pro20x":100,"pro50x":120},"products":[{"code":"pro50x","label":"Pro 50x","creditPrice":120,"enabled":false}],"productAvailability":{"pro50x":false}}}`)
	}))
	defer server.Close()
	v, err := New().Catalog(context.Background(), server.URL+"/prefix/api/v1/")
	if err != nil || len(v.Countries) != 2 || v.Countries[1].Currency != "PHP" || v.CreditPrices["pro50x"] != 120 || v.ProductAvailability["pro50x"] || len(v.Products) != 1 {
		t.Fatalf("catalog parse: %+v %v", v, err)
	}
}

func TestCMSNavCatalogRejectsIncompleteAndErrorResponses(t *testing.T) {
	for _, body := range []string{`{"code":0,"data":{}}`, `{"code":0,"data":{"countries":[{"code":"US"}]}}`, `<html>secret</html>`, `{"errorCode":"CHANNEL_UNAVAILABLE"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := New().Catalog(context.Background(), server.URL)
		server.Close()
		if err == nil {
			t.Fatal("accepted incomplete catalog")
		}
	}
}

// Read-only smoke check, explicitly enabled; never submits a payment or key.
func TestCMSNavCatalogLive(t *testing.T) {
	if os.Getenv("GPTPAY_TEST_PUBLIC_CATALOG") != "1" {
		t.Skip("public network test disabled")
	}
	v, err := New().Catalog(context.Background(), CMSNavURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Countries) < 2 || len(v.CreditPrices) == 0 {
		t.Fatal("incomplete public catalog")
	}
	t.Logf("Public catalog: %d countries, %d products", len(v.Countries), len(v.Products))
}
