package httpapi

import (
	"net/http"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestProAutomationSettingsSaveWithoutPushCredentials(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("saving config must not contact supplier") })
	pro := model.DefaultProSettings()
	pro.QuotaUsedThreshold, pro.QuotaCheckIntervalSeconds = 85, 45
	input := map[string]any{"automation": pro, "gptpay": map[string]any{"plan_code": "pro5"}}
	rec := payCall(t, s.saveProAutomationSettings, "", "", input)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _, _, _ := st.ProSettings()
	if saved.QuotaUsedThreshold != 85 || saved.QuotaCheckIntervalSeconds != 45 {
		t.Fatal("settings not persisted")
	}
	for _, threshold := range []float64{0, -1, 101} {
		pro.QuotaUsedThreshold = threshold
		input["automation"] = pro
		if rec = payCall(t, s.saveProAutomationSettings, "", "", input); rec.Code != 400 {
			t.Fatalf("accepted invalid threshold %v", threshold)
		}
	}
	pro.QuotaUsedThreshold, pro.AutoMergeEnabled = 85, true
	input["automation"] = pro
	if rec = payCall(t, s.saveProAutomationSettings, "", "", input); rec.Code != 400 {
		t.Fatal("auto merge accepted with no target")
	}
	saved, _, _, _ = st.ProSettings()
	if saved.QuotaUsedThreshold != 85 || saved.AutoMergeEnabled {
		t.Fatal("failed request changed settings")
	}
}
