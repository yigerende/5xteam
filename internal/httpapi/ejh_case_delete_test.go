package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/store"
)

func TestEJHDeleteUnopenedCasesAPI(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	enableEJHPaymentFixture(t, st)
	card, err := st.ImportEJHCard(ejh.Card{OrderNo: "delete-fixture", Number: "4242424242424242", Validity: "1299", CVV: "123", Status: "1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	settings, secret, err := st.EJHSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.SingleActiveAccount, settings.DefaultMaxAccounts = false, 100
	if _, err = st.SaveEJHSettings(settings, secret); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 14; i++ {
		id, status := fmt.Sprintf("delete-%d", i), "failed"
		if i == 12 {
			status = "processing"
		}
		if i == 13 {
			status = "success"
		}
		o := gptpay.Order{ID: id, Email: id + "@example.com", CardID: card.ID, Status: status, CreatedAt: now, UpdatedAt: now}
		snap := gptpay.Snapshot{APIKey: "sensitive-fixture", Input: gptpay.CreateInput{CardSecret: gptpay.CardSecret{Number: "4242424242424242", Year: 2099, Month: 12, CVV: "123"}}}
		if err = st.CreateGPTPayOrder(o, snap); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st, sessions: map[string]time.Time{"test": now.Add(time.Hour)}}
	mux := http.NewServeMux()
	s.ejhRoutes(mux)
	handler := s.authMiddleware(mux)
	call := func(method, path, body string, logged bool, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if logged {
			r.AddCookie(&http.Cookie{Name: authCookieName, Value: "test"})
		}
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	body := `{"confirm":true}`
	for _, check := range []struct {
		path, body string
		logged     bool
		origin     string
		code       int
	}{
		{"delete-0", body, false, "", 401},
		{"unopened", body, true, "https://unrelated.example", 403},
		{"delete-0", "{", true, "", 400},
		{"unopened", `{}`, true, "", 400},
		{"delete-13", body, true, "", 409},
		{"missing", body, true, "", 404},
	} {
		w := call("DELETE", "/api/ejh/cases/"+check.path, check.body, check.logged, check.origin)
		if w.Code != check.code {
			t.Fatal(check, w.Code, w.Body.String())
		}
	}
	deleteAndCheck := func(path string, count int) {
		t.Helper()
		w := call("DELETE", "/api/ejh/cases/"+path, body, true, "")
		var out struct {
			Data struct {
				Deleted int      `json:"deleted"`
				IDs     []string `json:"deleted_ids"`
			} `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Data.Deleted != count || len(out.Data.IDs) != count {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "sensitive-fixture") || strings.Contains(w.Body.String(), "4242424242424242") {
			t.Fatal("sensitive response")
		}
	}
	visible := func() []ejh.Case {
		t.Helper()
		w := call("GET", "/api/ejh/dashboard", "", true, "")
		var out struct {
			Data struct {
				Cases []ejh.Case `json:"cases"`
			} `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return out.Data.Cases
	}
	if n := len(visible()); n != 14 {
		t.Fatal(n)
	}
	lock := &sync.Mutex{}
	s.proLocks.Store("account:delete-1@example.com", lock)
	lock.Lock()
	w := call("DELETE", "/api/ejh/cases/unopened", body, true, "")
	lock.Unlock()
	if w.Code != 409 || len(visible()) != 14 {
		t.Fatal("partial deletion during payment", w.Code, w.Body.String())
	}
	// Earlier locks acquired before the conflict must have been released.
	v, _ := s.proLocks.Load("account:delete-0@example.com")
	if !v.(*sync.Mutex).TryLock() {
		t.Fatal("account lock leaked")
	}
	v.(*sync.Mutex).Unlock()
	deleteAndCheck("delete-0", 1)
	if n := len(visible()); n != 13 {
		t.Fatal(n)
	}
	deleteAndCheck("delete-0", 0)
	deleteAndCheck("unopened", 11) // Includes records beyond the default ten-row page.
	rows := visible()
	if len(rows) != 2 {
		t.Fatal("normal record lost", rows)
	}
	deleteAndCheck("unopened", 0)
	if err = st.EJHBackfillCases(); err != nil {
		t.Fatal(err)
	}
	if n := len(visible()); n != 2 {
		t.Fatal("backfill resurrected deleted entries", n)
	}
	order, _, err := st.GPTPayOrder("delete-12")
	if err != nil || !order.Active() {
		t.Fatal("pending payment order lost", order, err)
	}
	order.Status = "success"
	if err = st.UpdateGPTPayOrder(order); err != nil {
		t.Fatal(err)
	}
	if n := len(visible()); n != 2 {
		t.Fatal("later success hidden", n)
	}
}
