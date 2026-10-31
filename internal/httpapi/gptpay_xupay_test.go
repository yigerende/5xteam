package httpapi

import (
	"bytes"
	"chatgpt-space-merge/internal/gptpay"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestXUPayEndToEndOriginalOrderAndPaidFence(t *testing.T) {
	posts, gets := 0, 0
	activation, renewal := "pending", "pending"
	var original []byte
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xupay-key" {
			t.Error("wrong supplier credentials")
		}
		if r.Method == "GET" {
			gets++
			if r.URL.Path != "/api/v1/orders/xupay-order" {
				t.Error("wrong status endpoint")
			}
			fmt.Fprintf(w, `{"order":{"id":"xupay-order","product_id":"pro","workflow":"subscription","status":"succeeded","activation":%q,"renewal":%q,"fee":100},"payment":{"state":"succeeded","fee_state":"charged","amount":20000,"currency":"USD"}}`, activation, renewal)
			return
		}
		posts++
		if r.Header.Get("Idempotency-Key") != "pro-xupay-order-test001" || r.URL.Path != "/api/v1/orders" {
			t.Error("new id or endpoint on replay")
		}
		raw, _ := io.ReadAll(r.Body)
		if posts == 1 {
			original = raw
		} else if !bytes.Equal(raw, original) {
			t.Error("changed original payment payload")
		}
		switch posts {
		case 1:
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"fixture upstream timeout"}`)
		case 2:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":"rate limited before idempotency lookup"}`)
		default:
			fmt.Fprint(w, `{"id":"xupay-order","status":"succeeded","replayed":true}`)
		}
	})
	cfg, _, _ := st.GPTPaySettings()
	cfg.Provider, cfg.Country, cfg.TermsAccepted = gptpay.XUPay, "PH", true
	if _, err := st.SaveGPTPaySettings(cfg, "xupay-key"); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"request_id": "xupay-order-test001", "provider": gptpay.XUPay, "plan_code": "pro20", "card_id": "test-card"}
	if rec := payCall(t, s.createGPTPayOrder, "email", email, input); rec.Code != 400 || posts != 0 {
		t.Fatal("AT-only session must stop before a supplier submission")
	}
	p, c, err := st.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"accessToken": c.AccessToken, "session_token": "fixture-web-session", "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}})
	c.ChatGPTSession = string(raw)
	if _, err = st.SaveMailAccount(p, c); err != nil {
		t.Fatal(err)
	}
	o := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, input))
	if o.Status != "submission_unknown" || !o.SubmissionStarted || posts != 1 {
		t.Fatal(o)
	}
	// Change active supplier, then replay the immutable original XUPay request.
	if _, err := st.SaveGPTPaySettings(gptpay.Settings{PlanCode: "pro5", URL: "https://unused.invalid/api/v1"}, "other-key"); err != nil {
		t.Fatal(err)
	}
	o = payOrder(t, payCall(t, s.retryGPTPayOrder, "id", o.ID, map[string]any{}))
	if o.Status != "submission_unknown" || posts != 2 {
		t.Fatal("429 on replay released uncertain charge", o)
	}
	time.Sleep(time.Until(*o.NextStatusCheckAt) + 10*time.Millisecond)
	o = payOrder(t, payCall(t, s.retryGPTPayOrder, "id", o.ID, map[string]any{}))
	if o.Status != "processing" || o.Remote.ID != "xupay-order" {
		t.Fatal(o)
	}
	o = payOrder(t, payCall(t, s.refreshGPTPayOrder, "id", o.ID, map[string]any{}))
	if o.Status != "processing" || o.Remote.PaymentState != "succeeded" {
		t.Fatal("paid not yet active misclassified", o)
	}
	activation = "review"
	o = payOrder(t, payCall(t, s.refreshGPTPayOrder, "id", o.ID, map[string]any{}))
	if o.Status != "manual_review" || !o.RetryBlocked() {
		t.Fatal(o)
	}
	_ = payOrder(t, payCall(t, s.retryGPTPayOrder, "id", o.ID, map[string]any{}))
	if posts != 3 {
		t.Fatal("paid order submitted again")
	}
	activation, renewal = "active", "review"
	o = payOrder(t, payCall(t, s.refreshGPTPayOrder, "id", o.ID, map[string]any{}))
	if o.Status != "success" || o.Remote.CancellationStatus != "failed" || o.Remote.RetryHint == "" {
		t.Fatal(o)
	}
	if err := s.validateGPTPayNewPurchase(email, gptpay.Settings{}); err == nil {
		t.Fatal("provider switch allowed repurchase")
	}
	regressed := o
	regressed.Status, regressed.Remote.PaymentState = "failed", "failed"
	if err := st.UpdateGPTPayOrder(regressed); err == nil {
		t.Fatal("paid state was erased")
	}
	if posts != 3 || gets != 3 {
		t.Fatal(posts, gets)
	}
	o.SubmissionStarted = false
	if err := st.UpdateGPTPayOrder(o); err != nil {
		t.Fatal(err)
	}
	saved, _, err := st.GPTPayOrder(o.ID)
	if err != nil || !saved.SubmissionStarted {
		t.Fatal("submit fence was erased")
	}
	data := payCall(t, s.retryGPTPayOrder, "id", o.ID, map[string]any{}).Body.String()
	for _, secret := range []string{"xupay-key", "4242424242424242", "accessToken", "do-not-use-rt"} {
		if bytes.Contains([]byte(data), []byte(secret)) {
			t.Fatal("order projection leaked credentials")
		}
	}
}

func TestXUPayCatalogUsesSavedURLAndPrivateKey(t *testing.T) {
	calls := 0
	s, st, _ := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/catalog" || r.Header.Get("Authorization") != "Bearer private-key" {
			t.Error("catalog routing")
		}
		fmt.Fprint(w, `{"unit":"cent_R","payment_connected":true,"countries":[{"code":"PH","name":"菲律宾","currency":"PHP"}],"items":[{"id":"pro5x","member_fee":100}]}`)
	})
	cfg, _, _ := st.GPTPaySettings()
	cfg.Provider = gptpay.XUPay
	if _, err := st.SaveGPTPaySettings(cfg, "private-key"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.getGPTPayCatalog(rec, httptest.NewRequest("GET", "/api/gptpay/catalog?provider=xupay&url=https://untrusted.invalid/api/v1", nil))
	if rec.Code != 200 || calls != 1 || bytes.Contains(rec.Body.Bytes(), []byte("private-key")) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	settings := httptest.NewRecorder()
	s.getGPTPaySettings(settings, httptest.NewRequest("GET", "/api/gptpay/settings?provider=xupay", nil))
	var body struct {
		Data gptpay.Settings `json:"data"`
	}
	_ = json.Unmarshal(settings.Body.Bytes(), &body)
	if !body.Data.KeyPresent || bytes.Contains(settings.Body.Bytes(), []byte("private-key")) {
		t.Fatal("settings key exposure")
	}
}
