package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"chatgpt-space-merge/internal/gptpay"
)

func TestProCardLimitAPIAndScheduledCapacity(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("card configuration must not contact supplier") })
	configureProAutoFixture(t, s)
	card, secret, err := st.GPTPayCard("test-card")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveGPTPayCard(gptpay.Card{ID: "duplicate", Enabled: true}, secret); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{-1, 10001, 1.5, "2"} {
		w := payCall(t, s.saveGPTPayCard, "id", card.ID, map[string]any{"max_accounts": value})
		if w.Code != 400 {
			t.Fatalf("invalid limit %v: %d", value, w.Code)
		}
	}
	w := payCall(t, s.saveGPTPayCard, "id", card.ID, map[string]any{"max_accounts": 6})
	var response struct {
		Data gptpay.Card `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Data.AccountLimit != 6 || response.Data.MaxAccounts == nil || *response.Data.MaxAccounts != 6 {
		t.Fatal("save response", w.Body.String())
	}
	for _, enabled := range []bool{false, true} {
		w = payCall(t, s.saveGPTPayCard, "id", card.ID, map[string]any{"enabled": enabled})
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		current, _, _ := st.GPTPayCard(card.ID)
		if current.AccountLimit != 6 || current.Enabled != enabled {
			t.Fatal("toggle changed limit", current)
		}
	}
	status, err := s.proScheduleStatus()
	if err != nil || status.CardCapacity != 6 || status.AvailableCards != 1 {
		t.Fatal("scheduler double-counted duplicate or ignored custom limit", status, err)
	}
	if err = st.ReserveProCard("pending@example.com", card.ID, false); err != nil {
		t.Fatal(err)
	}
	status, err = s.proScheduleStatus()
	if err != nil || status.CardCapacity != 5 {
		t.Fatal("scheduler ignored pending", status, err)
	}
	w = payCall(t, s.saveGPTPayCard, "id", card.ID, map[string]any{"max_accounts": 0})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	status, err = s.proScheduleStatus()
	if err != nil || status.CardCapacity != 2 {
		t.Fatal("reset global capacity", status, err)
	}
	_, loaded, _ := st.GPTPayCard(card.ID)
	if loaded != secret {
		t.Fatal("editing limit changed payment credentials")
	}
}
