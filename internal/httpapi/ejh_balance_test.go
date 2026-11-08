package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/store"
)

type ejhBalanceTransport func(*http.Request) (*http.Response, error)

func (fn ejhBalanceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestEJHBalanceSnapshotsPreserveLastQuery(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.Close() }()
	cfg := ejh.DefaultSettings()
	cfg.Enabled = true
	cfg.AppKey = "fixture"
	if _, err = st.SaveEJHSettings(cfg, "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	remote := ejh.Card{OrderNo: "balance-fixture", Number: "4242424242424242", Validity: "1299", CVV: "123", Status: "1", CardCurrency: "USD"}
	card, err := st.ImportEJHCard(remote, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	s.ejh.client = ejh.New()
	response := `{"code":"000000","data":{"balance":"0.00"}}`
	calls := 0
	s.ejh.client.HTTP = &http.Client{Transport: ejhBalanceTransport(func(r *http.Request) (*http.Response, error) {
		body := response
		if r.URL.Path == "/api/v3/authenticate" {
			body = `{"code":"000000","data":{"authToken":"fixture","validity":3600000}}`
		} else {
			calls++
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	action := func(body string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/ejh/cards/x/action", strings.NewReader(body))
		r.SetPathValue("id", card.ID)
		w := httptest.NewRecorder()
		s.ejhCardAction(w, r)
		if w.Code != want {
			t.Fatalf("%d: %s", w.Code, w.Body.String())
		}
	}
	snapshot := func() ejh.CardRecord {
		t.Helper()
		var v ejh.CardRecord
		if e := st.EJHGet("card", card.ID, &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	action(`{"action":"balance"}`, 200)
	first := snapshot()
	if first.Balance != "0.00" || first.BalanceAt == nil {
		t.Fatal("zero balance not saved")
	}
	response = `{"code":"000000","data":{"balance":"-0.25"}}`
	action(`{"action":"balance"}`, 200)
	first = snapshot()
	response = `{"code":"000000","data":{"balance":"invalid"}}`
	action(`{"action":"balance"}`, 502)
	if got := snapshot(); got.Balance != "-0.25" || got.BalanceError == "" || !got.BalanceAt.Equal(*first.BalanceAt) {
		t.Fatal("failed query replaced snapshot")
	}
	remote.CardAmount = "999"
	if _, err = st.ImportEJHCard(remote, false); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(); got.Balance != "-0.25" || got.BalanceError == "" {
		t.Fatal("card refresh lost snapshot")
	}
	response = `{"code":"000000","data":{"orderNo":"balance-fixture","billAmount":"999"}}`
	action(`{"action":"increase","amount":"20","confirm":true,"request_id":"balance-increase-fixture"}`, 200)
	if got := snapshot(); got.Balance != "-0.25" || !got.BalanceAt.Equal(*first.BalanceAt) {
		t.Fatal("increase changed manual snapshot")
	}
	response = `{"code":"000000","data":{"abroadList":[{"currency":"USD","balance":"12.10"},{"currency":"EUR","balance":"0"}]}}`
	w := httptest.NewRecorder()
	s.testEJH(w, httptest.NewRequest("POST", "/api/ejh/balance", strings.NewReader("{}")))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var account struct {
		Data struct {
			Balances []struct {
				Currency string `json:"currency"`
				Balance  string `json:"balance"`
			} `json:"abroadList"`
		} `json:"data"`
		At time.Time `json:"at"`
	}
	if err = st.EJHGet("balance", "latest", &account); err != nil || len(account.Data.Balances) != 2 {
		t.Fatal("account snapshot not saved", err)
	}
	response = `{"code":"000000","data":{}}`
	w = httptest.NewRecorder()
	s.testEJH(w, httptest.NewRequest("POST", "/api/ejh/balance", strings.NewReader("{}")))
	if w.Code != 502 {
		t.Fatal("missing balance accepted")
	}
	count := calls
	for i := 0; i < 2; i++ {
		w = httptest.NewRecorder()
		s.ejhDashboard(w, httptest.NewRequest("GET", "/api/ejh/dashboard", nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "12.10") {
			t.Fatal("dashboard lost saved balance")
		}
	}
	if calls != count {
		t.Fatal("dashboard queried supplier")
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.store = st
	if got := snapshot(); got.Balance != "-0.25" || got.BalanceAt == nil {
		t.Fatal("restart lost snapshot")
	}
	// Page reads still use snapshots; the automatic balance pass is tested separately.
}
