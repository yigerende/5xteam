package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
)

func TestStarBridgeMerchantRecoveryAndPolling(t *testing.T) {
	f := merchantFixture(t)
	posts, gets := 0, 0
	var body, key string
	status := "queued"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer bridge-key" {
			t.Error("wrong supplier key")
		}
		if r.Method == "POST" {
			posts++
			raw, _ := io.ReadAll(r.Body)
			if posts == 1 {
				body, key = string(raw), r.Header.Get("Idempotency-Key")
				w.WriteHeader(502)
				return
			}
			if string(raw) != body || key != r.Header.Get("Idempotency-Key") {
				t.Error("lost response retry changed request")
			}
			var in refundmerchant.Submission
			if json.Unmarshal(raw, &in) != nil || !in.Authorized || len(in.Accounts) != 1 || in.Accounts[0].Email != f.email || in.Accounts[0].Password != "fixture-password" {
				t.Error("incorrect account payload")
			}
			fmt.Fprint(w, `{"ids":["bridge-order"],"replayed":true}`)
			return
		}
		gets++
		fmt.Fprintf(w, `{"order":{"id":"bridge-order","email":%q,"status":%q,"fee":5000,"fee_status":"settled"},"events":[]}`, f.email, status)
	}))
	defer srv.Close()
	v, _, _, _ := f.s.store.ProSettings()
	v.MerchantProvider = refundmerchant.ProviderStarBridge
	v.MerchantStarBridgeAPIURL = srv.URL + "/api/v1/refund/orders"
	v.MerchantSuccessFeeUSD = "1.25"
	pay := gptpay.Settings{PlanCode: "pro5"}
	if _, _, err := f.s.store.SaveProAutomationSettings(v, pay, "", "", "bridge-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.performProMerchant(t.Context(), f.email); err == nil {
		t.Fatal("ignored lost response")
	}
	v.MerchantProvider = refundmerchant.ProviderOriginal
	v.MerchantStarBridgeAPIURL = "http://127.0.0.1:1/no-call"
	v.MerchantSuccessFeeUSD = "9.00"
	if _, _, err := f.s.store.SaveProAutomationSettings(v, pay, "", "", "changed-key"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.RecoverProWorkflows(); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.performProMerchant(t.Context(), f.email)
	if err != nil || !p.MerchantAccepted() || p.MerchantCompleted() || gets != 0 || p.ProMerchant.Provider != "starbridge" {
		t.Fatal("acceptance incorrectly completed", err)
	}
	for _, next := range []string{"queued", "processing", "verifying", "attention", "succeeded"} {
		status = next
		p, err = f.s.performProMerchant(t.Context(), f.email)
		if err != nil || p.MerchantCompleted() != (next == "succeeded") || p.ProDeactivation != nil || p.SpaceMergedOnce {
			t.Fatal("incorrect progress", next, err)
		}
	}
	if _, err = f.s.performProMerchant(t.Context(), f.email); err != nil {
		t.Fatal(err)
	}
	if posts != 2 || gets != 5 || p.ProMerchant.SuccessFeeUSD != "1.25" || len(f.calls) != 0 {
		t.Fatal("repeated submission or changed cost", posts, gets)
	}
}

func TestStarBridgeMerchantAPISettingsIsolation(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream call") })
	pro := model.DefaultProSettings()
	pro.AutoMerchantEnabled = true
	input := map[string]any{"automation": pro, "gptpay": map[string]any{"plan_code": "pro5"}, "merchant_api_key": "original-test-key"}
	if rec := payCall(t, s.saveProAutomationSettings, "", "", input); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	pro.MerchantProvider = "starbridge"
	input["automation"] = pro
	input["merchant_api_key"] = ""
	if rec := payCall(t, s.saveProAutomationSettings, "", "", input); rec.Code != 400 {
		t.Fatal("enabled StarBridge without its key", rec.Code)
	}
	input["merchant_starbridge_api_key"] = "bridge-test-key"
	if rec := payCall(t, s.saveProAutomationSettings, "", "", input); rec.Code != 200 || strings.Contains(rec.Body.String(), "bridge-test-key") {
		t.Fatal("save failed or leaked key", rec.Code)
	}
	// Legacy clients and unrelated push settings must not replace this selection.
	input["automation"] = map[string]any{"quota_used_threshold": 100, "quota_check_interval_seconds": 120, "concurrency": 2, "retry_interval_seconds": 3, "target_seat_type": "default"}
	input["merchant_starbridge_api_key"] = ""
	if rec := payCall(t, s.saveProAutomationSettings, "", "", input); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	push := map[string]any{"provider": "cpa", "cpa": map[string]any{"url": "http://cpa.invalid"}, "cpa_key": "test", "merchant_provider": "original"}
	if rec := payCall(t, s.saveProSettings, "", "", push); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _, _, err := st.ProSettings()
	if err != nil || saved.MerchantProvider != "starbridge" || !saved.MerchantKeyPresent || !saved.MerchantStarBridgeKeyPresent {
		t.Fatal("configuration changed", err)
	}
}
