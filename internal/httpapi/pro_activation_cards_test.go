package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
)

func TestProAccountListsIncludeActivationCard(t *testing.T) {
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("listing must not request supplier") })
	order := gptpay.Order{ID: "prior-success", Email: email, Status: "success", CardName: "用于开通的卡", CardLast4: "4242", CreatedAt: time.Now()}
	if err := st.CreateGPTPayOrder(order, gptpay.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/pro-accounts", "/api/pro-accounts?page=1&page_size=10"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		s.listProAccounts(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		var items []proAccountListItem
		if strings.Contains(path, "?") {
			var page struct {
				Items []proAccountListItem `json:"items"`
				Total int                  `json:"total"`
			}
			if err := json.Unmarshal(envelope.Data, &page); err != nil {
				t.Fatal(err)
			}
			if page.Total != 1 {
				t.Fatal("changed pagination total")
			}
			items = page.Items
		} else if err := json.Unmarshal(envelope.Data, &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Email != email || items[0].ActivationCard == nil || items[0].ActivationCard.CardLast4 != "4242" {
			t.Fatal("card missing from account list")
		}
		if strings.Contains(w.Body.String(), "encrypted_snapshot") || strings.Contains(w.Body.String(), "cvv") {
			t.Fatal("payment secrets exposed")
		}
	}
}
