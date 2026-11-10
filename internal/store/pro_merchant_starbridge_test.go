package store

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
)

func TestStarBridgeMerchantSettingsAndConcurrentSnapshot(t *testing.T) {
	s := transferStore(t)
	v := model.DefaultProSettings()
	v.AutoMerchantEnabled = true
	pay := gptpay.Settings{PlanCode: "pro5"}
	if _, _, err := s.SaveProAutomationSettings(v, pay, "", "original-secret"); err != nil {
		t.Fatal(err)
	}
	v.MerchantProvider = refundmerchant.ProviderStarBridge
	if _, _, err := s.SaveProAutomationSettings(v, pay, ""); err == nil {
		t.Fatal("reused original key for StarBridge")
	}
	v.MerchantStarBridgeAPIURL = "https://bridge.example.com/api/v1/refund/orders"
	if _, _, err := s.SaveProAutomationSettings(v, pay, "", "", "bridge-secret"); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"pinned@example.com", "new@example.com"} {
		if _, err := s.SaveMailAccount(model.MailAccountProfile{Email: email}, credentialsForMerchantTest(email)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateMailAccountManagementScope(email, "pro"); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	ids := make(chan string, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, sub, err := s.PrepareProMerchant("pinned@example.com")
			if err != nil {
				t.Error(err)
				return
			}
			if sub.Provider != refundmerchant.ProviderStarBridge || sub.APIKey != "bridge-secret" || sub.APIURL != v.MerchantStarBridgeAPIURL {
				t.Error("wrong snapshot")
			}
			ids <- p.ProMerchant.RequestID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for v := range ids {
		if id != "" && id != v {
			t.Fatal("concurrent duplicate request")
		}
		id = v
	}
	v.MerchantProvider = refundmerchant.ProviderOriginal
	v.MerchantStarBridgeAPIURL = "https://replacement.example.com/refunds"
	if _, _, err := s.SaveProAutomationSettings(v, pay, "", "", "bridge-new-secret"); err != nil {
		t.Fatal(err)
	}
	p, sub, err := s.PrepareProMerchant("pinned@example.com")
	if err != nil || p.ProMerchant.RequestID != id || sub.Provider != refundmerchant.ProviderStarBridge || sub.APIKey != "bridge-secret" || strings.Contains(sub.APIURL, "replacement") {
		t.Fatal("changed pinned supplier", err)
	}
	_, sub, err = s.PrepareProMerchant("new@example.com")
	if err != nil || sub.Provider != refundmerchant.ProviderOriginal || sub.APIKey != "original-secret" {
		t.Fatal("did not restore original key", err)
	}
	saved, _, _, err := s.ProSettings()
	if err != nil || !saved.MerchantKeyPresent || !saved.MerchantStarBridgeKeyPresent {
		t.Fatal("missing key flags", err)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("key exposed in settings")
	}
	var cipher string
	if err = s.db.QueryRow("SELECT encrypted_key FROM pro_starbridge_merchant_secret WHERE id=1").Scan(&cipher); err != nil || strings.Contains(cipher, "bridge-new-secret") {
		t.Fatal("key was not encrypted", err)
	}
}
