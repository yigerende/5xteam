package httpapi

import (
	"bytes"
	"chatgpt-space-merge/internal/gptpay"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGPTPayCMSNavManualReplayUsesOriginalProvider(t *testing.T) {
	var calls int
	var original []byte
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cmsnav-key" {
			t.Error("payment used changed supplier credentials")
		}
		if r.Method == "GET" {
			fmt.Fprint(w, `{"code":0,"data":{"order":{"status":"success"}}}`)
			return
		}
		if r.URL.Path != "/api/v1/customer/recharges" || r.Header.Get("Idempotency-Key") != "pro-cmsnav-original-001" {
			t.Error("wrong original payment route or id")
		}
		var body json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls++
		if calls == 1 {
			original = body
			w.WriteHeader(503)
			fmt.Fprint(w, `{"errorCode":"CHANNEL_UNAVAILABLE"}`)
			return
		}
		if !bytes.Equal(original, body) {
			t.Error("retry altered original payment input")
		}
		fmt.Fprint(w, `{"code":0,"data":{"taskId":"cms-task","status":"reserved"}}`)
	})
	cfg, _, _ := st.GPTPaySettings()
	cfg.Provider, cfg.PlanCode, cfg.Country = gptpay.CMSNav, "pro50", "US"
	if _, err := st.SaveGPTPaySettings(cfg, "cmsnav-key"); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"request_id": "cmsnav-original-001", "provider": gptpay.CMSNav, "plan_code": "pro50", "card_id": "test-card"}
	if rec := payCall(t, s.createGPTPayOrder, "email", email, input); rec.Code != 400 || calls != 0 {
		t.Fatal("missing web session should be rejected before payment")
	}
	p, c, _ := st.MailAccountCredential(email)
	raw, _ := json.Marshal(map[string]any{"accessToken": c.AccessToken, "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}, "expires": "2099-01-01T00:00:00Z"})
	c.ChatGPTSession = string(raw)
	if _, err := st.SaveMailAccount(p, c); err != nil {
		t.Fatal(err)
	}
	stale := map[string]any{"request_id": "cmsnav-stale-00001", "plan_code": "pro50", "card_id": "test-card"}
	if rec := payCall(t, s.createGPTPayOrder, "email", email, stale); rec.Code != 409 || calls != 0 {
		t.Fatal("stale provider was accepted")
	}
	o := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, input))
	if o.Status != "submission_unknown" || o.Provider != gptpay.CMSNav || calls != 1 {
		t.Fatal(o)
	}
	if _, err := st.SaveGPTPaySettings(gptpay.Settings{PlanCode: "pro20", URL: "https://unused.invalid/api/v1"}, "different-key"); err != nil {
		t.Fatal(err)
	}
	o = payOrder(t, payCall(t, s.retryGPTPayOrder, "id", o.ID, map[string]any{}))
	if o.Status != "processing" || calls != 2 {
		t.Fatal(o)
	}
	o = payOrder(t, payCall(t, s.refreshGPTPayOrder, "id", o.ID, map[string]any{}))
	if o.Status != "success" || o.Remote.ID != "cms-task" {
		t.Fatal(o)
	}
	_, snapshot, err := st.GPTPayOrder(o.ID)
	if err != nil || snapshot.Provider != gptpay.CMSNav || snapshot.Country != "US" || snapshot.Input.PlanCode != "pro50" {
		t.Fatal("immutable provider snapshot lost")
	}
	// A duplicate browser submission returns the original order, even after switching.
	_ = payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, input))
	if calls != 2 {
		t.Fatal("duplicate browser request charged twice")
	}
	query := httptest.NewRecorder()
	s.getGPTPaySettings(query, httptest.NewRequest("GET", "/api/gptpay/settings?provider=cmsnav", nil))
	if query.Code != 200 || bytes.Contains(query.Body.Bytes(), []byte("cmsnav-key")) {
		t.Fatal("provider settings exposed secret")
	}
}
