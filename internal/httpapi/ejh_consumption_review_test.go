package httpapi

import (
	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEJHConsumptionReviewAPI(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	enableEJHPaymentFixture(t, st)
	card, err := st.ImportEJHCard(ejh.Card{OrderNo: "review", Number: "4242424242424242", Validity: "1299", CVV: "123", Status: "1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	email := "review@example.com"
	now := time.Now()
	if _, err = st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, GptPassword: "private-test-password"}); err != nil {
		t.Fatal(err)
	}
	st.UpdateMailAccountManagementScope(email, "pro")
	order := gptpay.Order{ID: "review", Email: email, CardID: card.ID, Status: "success", CreatedAt: now, UpdatedAt: now}
	snap := gptpay.Snapshot{APIKey: "private-api-key", Input: gptpay.CreateInput{CardSecret: gptpay.CardSecret{Number: "4242424242424242", Year: 2099, Month: 12, CVV: "123"}}}
	if err = st.CreateGPTPayOrder(order, snap); err != nil {
		t.Fatal(err)
	}
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "flow", OrderID: order.ID, Status: "completed", Stage: "merchant", Steps: map[string]string{"push": "interrupted"}}
	})
	rows, err := st.EJHCases()
	if err != nil {
		t.Fatal(err)
	}
	rev := int64(0)
	in := store.EJHConsumptionReviewInput{State: "not_pushed", Reason: "已核实原下游没有创建账号", Confirm: true, ExpectedKey: rows[0].ConsumptionReviewKey, ExpectedRevision: &rev}
	raw, _ := json.Marshal(in)
	s := &Server{store: st, sessions: map[string]time.Time{"test": now.Add(time.Hour)}}
	mux := http.NewServeMux()
	s.ejhRoutes(mux)
	handler := s.authMiddleware(mux)
	call := func(body string, logged bool, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/ejh/cases/review/consumption-review", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if logged {
			r.AddCookie(&http.Cookie{Name: authCookieName, Value: "test"})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call(string(raw), false, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call(string(raw), true, "https://foreign.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call("{", true, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	lock := &sync.Mutex{}
	s.proLocks.Store("account:"+email, lock)
	lock.Lock()
	w := call(string(raw), true, "")
	lock.Unlock()
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	w = call(string(raw), true, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{"private-test-password", "private-api-key", "4242424242424242"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret in response")
		}
	}
	if w = call(string(raw), true, ""); w.Code != 409 {
		t.Fatal("stale duplicate accepted", w.Code)
	}
}
