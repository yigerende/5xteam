package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
)

func TestGPTPayEncryptedPersistenceAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := "fixture-supplier-api-key"
	token := "fixture-access-token-not-in-profile"
	if _, err = s.SaveGPTPaySettings(gptpay.Settings{PlanCode: "pro20"}, key); err != nil {
		t.Fatal(err)
	}
	secret := gptpay.CardSecret{Number: "4242424242424242", Month: 12, Year: 2099, CVV: "012"}
	if _, err = s.SaveGPTPayCard(gptpay.Card{ID: "card", Name: "Card", Enabled: true}, secret); err != nil {
		t.Fatal(err)
	}
	snapshot := gptpay.Snapshot{URL: gptpay.DefaultURL, APIKey: key, Input: gptpay.CreateInput{PlanCode: "pro20", CardSecret: secret, Session: gptpay.Session{AccessToken: token}}}
	o := gptpay.Order{ID: "order", Email: "test@example.com", Status: "submitting", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = s.CreateGPTPayOrder(o, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SELECT profile,encrypted_key FROM gptpay_settings", "SELECT profile,encrypted_secret FROM gptpay_cards", "SELECT profile,encrypted_snapshot FROM gptpay_orders"} {
		var raw, sealed string
		if err = s.db.QueryRow(q).Scan(&raw, &sealed); err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{key, token, secret.Number, `"cvv":"012"`} {
			if strings.Contains(raw+sealed, value) {
				t.Fatal("stored plaintext payment credential")
			}
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, savedKey, err := s.GPTPaySettings()
	if err != nil || savedKey != key || v.PlanCode != "pro20" {
		t.Fatal("settings lost on restart")
	}
	_, savedSecret, err := s.GPTPayCard("card")
	if err != nil || savedSecret != secret {
		t.Fatal("card lost on restart")
	}
	loaded, replay, err := s.GPTPayOrder("order")
	if err != nil || loaded.Status != "submitting" {
		t.Fatal("order lost on restart")
	}
	before, _ := json.Marshal(snapshot)
	after, _ := json.Marshal(replay)
	if string(before) != string(after) {
		t.Fatal("immutable request snapshot changed")
	}
	duplicate := o
	duplicate.ID = "other"
	if err = s.CreateGPTPayOrder(duplicate, snapshot); err == nil {
		t.Fatal("allowed a second active payment after restart")
	}
	loaded.Status = "success"
	if err = s.UpdateGPTPayOrder(loaded); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateGPTPayOrder(duplicate, snapshot); err != nil {
		t.Fatal("completed order should not block explicit future purchase")
	}
}
