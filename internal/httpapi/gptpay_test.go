package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func gptPayFixture(t *testing.T, handler http.HandlerFunc) (*Server, *store.Store, string) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	email := "pro-test@example.com"
	claims := fmt.Sprintf(`{"exp":%d,"https://api.openai.com/profile":{"email":%q},"https://api.openai.com/auth":{"chatgpt_account_id":"personal-id","chatgpt_user_id":"user-id"}}`, time.Now().Add(time.Hour).Unix(), email)
	at := "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
	if _, err = st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: at, RefreshToken: "do-not-use-rt"}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateMailAccountManagementScope(email, "pro"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveGPTPaySettings(gptpay.Settings{URL: upstream.URL, PlanCode: "pro20"}, "test-api-key"); err != nil {
		t.Fatal(err)
	}
	card := gptpay.Card{ID: "test-card", Name: "测试卡", Enabled: true}
	if _, err = st.SaveGPTPayCard(card, gptpay.CardSecret{Number: "4242424242424242", Month: 12, Year: 2099, CVV: "012"}); err != nil {
		t.Fatal(err)
	}
	return s, st, email
}
func payCall(t *testing.T, handler http.HandlerFunc, pathKey, pathValue string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/test", bytes.NewReader(raw))
	req.SetPathValue(pathKey, pathValue)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}
func payOrder(t *testing.T, rec *httptest.ResponseRecorder) gptpay.Order {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("HTTP %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data gptpay.Order `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}
func payInput(id string) map[string]any {
	return map[string]any{"request_id": id, "card_id": "test-card", "plan_code": "pro20"}
}

func TestGPTPayCreateStatusAndConcurrentDuplicate(t *testing.T) {
	var calls atomic.Int32
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "test-api-key" {
			t.Error("API key missing")
		}
		if r.URL.Path == "/api/v1/gpt-recharge/orders/status" {
			fmt.Fprint(w, `{"code":0,"data":{"orders":[{"id":"remote-order","status":"success","planCode":"pro20","settlementStatus":"settled","cancellationStatus":"success","chargedCredits":100}]}}`)
			return
		}
		if r.URL.Path != "/api/v1/gpt-recharge/orders" {
			t.Error("unexpected endpoint " + r.URL.Path)
		}
		calls.Add(1)
		var input gptpay.CreateInput
		json.NewDecoder(r.Body).Decode(&input)
		if input.PlanCode != "pro20" || input.Number != "4242424242424242" || input.CVV != "012" || input.Month != 12 || input.Year != 2099 || input.Session.Account.ID != "personal-id" || input.Session.User.Email != "pro-test@example.com" || !strings.HasPrefix(input.Session.AccessToken, "e30.") {
			t.Error("wrong payload")
		}
		if !strings.HasPrefix(r.Header.Get("Idempotency-Key"), "pro-") {
			t.Error("missing idempotency key")
		}
		time.Sleep(40 * time.Millisecond)
		w.WriteHeader(202)
		fmt.Fprint(w, `{"code":0,"data":{"id":"remote-order","status":"processing","planCode":"pro20","cancellationStatus":"waiting"}}`)
	})
	_, before, _ := st.MailAccountCredential(email)
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- payCall(t, s.createGPTPayOrder, "email", email, payInput(fmt.Sprintf("parallel-request-%02d", i)))
		}(i)
	}
	wg.Wait()
	close(results)
	var first gptpay.Order
	for rec := range results {
		order := payOrder(t, rec)
		if first.ID == "" {
			first = order
		}
		if order.ID != first.ID || order.Status != "processing" {
			t.Fatal("duplicate payment or wrong status")
		}
		for _, secret := range []string{before.AccessToken, before.RefreshToken, "4242424242424242", "test-api-key", `"cvv"`} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatal("secret exposed in response")
			}
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d purchases", calls.Load())
	}
	final := payOrder(t, payCall(t, s.refreshGPTPayOrder, "id", first.ID, map[string]any{}))
	if final.Status != "success" || final.Remote.ChargedCredits != 100 {
		t.Fatal("status not saved")
	}
	_, after, _ := st.MailAccountCredential(email)
	if before.AccessToken != after.AccessToken || before.RefreshToken != after.RefreshToken {
		t.Fatal("purchase must not refresh or overwrite tokens")
	}
	if _, err := st.ActiveGPTPayOrder(email); err == nil {
		t.Fatal("terminal order still active")
	}
}

func TestGPTPayUncertainReplayUsesOriginalSnapshot(t *testing.T) {
	var calls int
	var original, originalKey string
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		calls++
		if calls == 1 {
			original = string(body)
			originalKey = r.Header.Get("Idempotency-Key")
			w.WriteHeader(503)
			fmt.Fprint(w, `{"code":50301}`)
			return
		}
		if string(body) != original || r.Header.Get("Idempotency-Key") != originalKey || r.Header.Get("X-API-Key") != "test-api-key" {
			t.Error("retry changed payment or credentials")
		}
		fmt.Fprint(w, `{"code":0,"data":{"id":"remote-recovered","status":"processing"}}`)
	})
	o := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, payInput("uncertain-request-0001")))
	if o.Status != "submission_unknown" {
		t.Fatal(o.Status)
	}
	if err := st.DeleteGPTPayCard("test-card"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveGPTPaySettings(gptpay.Settings{PlanCode: "pro5"}, "new-api-key"); err != nil {
		t.Fatal(err)
	}
	// Clicking again resumes the existing local record, never submits a new key.
	second := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, payInput("uncertain-request-0002")))
	if second.ID != o.ID || calls != 1 {
		t.Fatal("created a duplicate")
	}
	recovered := payOrder(t, payCall(t, s.retryGPTPayOrder, "id", o.ID, map[string]any{}))
	if recovered.Remote.ID != "remote-recovered" || calls != 2 {
		t.Fatal("recovery failed")
	}
	payOrder(t, payCall(t, s.retryGPTPayOrder, "id", o.ID, map[string]any{}))
	if calls != 2 {
		t.Fatal("retried an accepted order")
	}
}

func TestGPTPayDisabledCardAndExpiredATBlockBeforeNetwork(t *testing.T) {
	var calls int
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	card, secret, _ := st.GPTPayCard("test-card")
	card.Enabled = false
	st.SaveGPTPayCard(card, secret)
	if rec := payCall(t, s.createGPTPayOrder, "email", email, payInput("disabled-request-001")); rec.Code != 400 {
		t.Fatal(rec.Body.String())
	}
	card.Enabled = true
	st.SaveGPTPayCard(card, secret)
	_, credentials, _ := st.MailAccountCredential(email)
	expired := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1}`)) + ".sig"
	credentials.AccessToken = expired
	st.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, credentials)
	if rec := payCall(t, s.createGPTPayOrder, "email", email, payInput("expired-request-0001")); rec.Code != 400 || !strings.Contains(rec.Body.String(), "已过期") {
		t.Fatal(rec.Body.String())
	}
	if calls != 0 {
		t.Fatal("invalid purchase called supplier")
	}
}

func TestGPTPayBankCRUDAndPagination(t *testing.T) {
	s, _, _ := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected provider request") })
	for i := 0; i < 11; i++ {
		rec := payCall(t, s.saveGPTPayCard, "id", "", map[string]any{"name": fmt.Sprintf("卡%d", i), "raw": "4242424242424242----209912----012", "enabled": true})
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "4242424242424242") {
			t.Fatal(rec.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "/cards?page=2&page_size=10", nil)
	rec := httptest.NewRecorder()
	s.listGPTPayCards(rec, req)
	var result struct {
		Data struct {
			Total int           `json:"total"`
			Items []gptpay.Card `json:"items"`
		} `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &result)
	if result.Data.Total != 12 || len(result.Data.Items) != 2 {
		t.Fatal("cards not truly paginated", rec.Body.String())
	}
	if out := payCall(t, s.saveGPTPayCard, "id", "test-card", map[string]any{"enabled": false}); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	if out := payCall(t, s.saveGPTPayCard, "id", "test-card", map[string]any{"name": "已修改", "exp_month": 11, "exp_year": 2099}); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	if out := payCall(t, s.deleteGPTPayCard, "id", "test-card", nil); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
}
