package httpapi

import (
	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/store"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readEJHTestView(t *testing.T, s *Server, query string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.ejhView(w, httptest.NewRequest("GET", "/api/ejh/view?"+query, nil))
	if w.Code != 200 {
		t.Fatalf("view: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Data
}
func viewIDs(out map[string]any) []string {
	ids := []string{}
	for _, v := range out["items"].([]any) {
		ids = append(ids, v.(map[string]any)["id"].(string))
	}
	return ids
}
func TestEJHViewPagingDependenciesAndFilters(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("saved view contacted issuer") })
	data, err := s.readEJHDashboard(false)
	if err != nil {
		t.Fatal(err)
	}
	card := func(id, status string, pending bool) ejhDisplayCard {
		return ejhDisplayCard{CardRecord: ejh.CardRecord{ID: id, NormalPending: pending, Remote: ejh.Card{OrderNo: id, Status: status}}, Linked: true, Local: &gptpay.Card{}}
	}
	data["cards"] = []ejhDisplayCard{card("normal", "1", false), card("pending", "1", true), card("closed-old", "2", false), card("closing", "8", false), card("closed-new", "2", false), card("unknown", "1", false)}
	rows := data["cards"].([]ejhDisplayCard)
	rows[5].Local.IssuerCancelStatus = "unknown"
	data["cases"] = []ejh.Case{{ID: "unopened", CardID: "normal", State: "unopened", Email: "find@example.com"}, {ID: "returned", CardID: "normal", State: "returned"}, {ID: "other", CardID: "closed-old", State: "waiting"}}
	now := time.Now()
	data["operations"] = []ejh.Operation{{ID: "old", Method: "cancel", CardID: "closed-old", Status: "completed", UpdatedAt: now.Add(-time.Hour)}, {ID: "new", Method: "cancel", CardID: "closed-new", Status: "completed", UpdatedAt: now}}
	data["trades"] = []ejh.TradeRecord{{ID: "refund-today", CardID: "normal", Trade: ejh.Trade{Type: "5", CreatedAt: "2026-11-04 08:00:00", TradeTime: "2026-11-02 10:00:00"}}, {ID: "refund-yesterday", CardID: "normal", Trade: ejh.Trade{Type: "5", CreatedAt: "2026-11-03 08:00:00", TradeTime: "2026-11-03 08:00:00"}}}
	rev, err := st.ReadRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s.ejh.views.data = data
	s.ejh.views.revision = rev
	out := readEJHTestView(t, s, "section=cards&lifecycle=closed&page_size=2")
	if got := viewIDs(out); !reflect.DeepEqual(got, []string{"closing", "unknown"}) {
		t.Fatal("pending first", got)
	}
	out = readEJHTestView(t, s, "section=cards&lifecycle=closed&page_size=2&page=2")
	if got := viewIDs(out); !reflect.DeepEqual(got, []string{"closed-new", "closed-old"}) {
		t.Fatal("successful cancellation order", got)
	}
	out = readEJHTestView(t, s, "section=cards&lifecycle=normal&q=find@example.com&page_size=1")
	if got := viewIDs(out); !reflect.DeepEqual(got, []string{"normal"}) {
		t.Fatal(got)
	}
	if len(out["cases"].([]any)) != 2 || len(out["trades"].([]any)) != 2 {
		t.Fatal("truncated current-card accounting")
	}
	out = readEJHTestView(t, s, "section=cases&category=unopened&page=20&page_size=1")
	if got := viewIDs(out); !reflect.DeepEqual(got, []string{"unopened"}) || out["page"] != float64(1) {
		t.Fatal("page clamp", out)
	}
	if len(out["cases"].([]any)) != 2 {
		t.Fatal("missing admission dependency")
	}
	out = readEJHTestView(t, s, "section=cards&lifecycle=normal&card_id=closed-old")
	if got := viewIDs(out); !reflect.DeepEqual(got, []string{"closed-old"}) {
		t.Fatal("exact-card navigation", got)
	}
	out = readEJHTestView(t, s, "section=trades&status=pending&page_size=1")
	if got := viewIDs(out); !reflect.DeepEqual(got, []string{"refund-today"}) {
		t.Fatal("issuer posting order", got)
	}
	out = readEJHTestView(t, s, "section=balance_targets&page_size=1")
	if len(out["cards"].([]any)) != 2 {
		t.Fatal("balance targets limited by pagination or include closed/pending")
	}
	out = readEJHTestView(t, s, "section=pending_targets&platform=oai")
	if len(out["cards"].([]any)) != 1 {
		t.Fatal("pending target selection")
	}
	data["refund_fees"] = []store.EJHRefundFee{{ID: "cross-card-fee", TradeIDs: []string{"other-fee-trade"}}}
	data["trades"] = append(data["trades"].([]ejh.TradeRecord), ejh.TradeRecord{ID: "other-fee-trade", CardID: "closed-old"})
	out = readEJHTestView(t, s, "section=card&card_id=normal")
	if len(out["trades"].([]any)) != 3 {
		t.Fatal("cross-card fee allocation lost its dependency")
	}
	// Labels used by account-return filters still honor payment-only policies.
	data["payment_policies"] = []store.CardPaymentPolicy{{CardID: "normal", Mode: "payment_only"}}
	out = readEJHTestView(t, s, "section=cases&status=no_return")
	if got := viewIDs(out); !reflect.DeepEqual(got, []string{"returned"}) {
		t.Fatal("policy label", got)
	}
}

func TestEJHViewOverviewPreservesLegacyFinancialInputs(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("read contacted issuer") })
	card := ejh.Card{OrderNo: "saved", Number: "5555555555554444", CVV: "919", Validity: "1299", Status: "1"}
	if _, err := st.ImportEJHCard(card, true); err != nil {
		t.Fatal(err)
	}
	before, err := s.readEJHDashboard(true)
	if err != nil {
		t.Fatal(err)
	}
	// Slow upstream synchronization must not prevent saved lists, totals or revision reads.
	s.ejh.mu.Lock()
	defer s.ejh.mu.Unlock()
	w := httptest.NewRecorder()
	s.ejhOverview(w, httptest.NewRequest("GET", "/api/ejh/overview", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), card.Number) || strings.Contains(w.Body.String(), `"cvvPassword":"919"`) {
		t.Fatal("overview failed or leaked secrets")
	}
	var out struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	for _, key := range []string{"cards", "operations", "trades", "cases", "balance", "recharge_ledger", "refund_fees", "reversal_fee_percent", "consumption_history", "merchant_fees", "payment_policies"} {
		want, _ := json.Marshal(before[key])
		if string(out.Data[key]) != string(want) {
			t.Fatal("financial input changed", key)
		}
	}
	beforeRevision := string(out.Data["revision"])
	if err := st.EJHPut("balance", "latest", map[string]any{"at": "new"}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.ejhOverview(w, httptest.NewRequest("GET", "/api/ejh/overview", nil))
	json.Unmarshal(w.Body.Bytes(), &out)
	if string(out.Data["revision"]) == beforeRevision || !strings.Contains(string(out.Data["balance"]), "new") {
		t.Fatal("cached totals not invalidated")
	}
	w = httptest.NewRecorder()
	s.ejhOperationProgress(w, httptest.NewRequest("GET", "/api/ejh/operations?ids=not-persisted-yet", nil))
	if w.Code != 200 {
		t.Fatal("pending operation lookup failed")
	}
}

func TestEJHViewCoalescesCancelledAndConcurrentReaders(t *testing.T) {
	var cache ejhViewCache
	var n atomic.Int32
	var version atomic.Int32
	version.Store(1)
	entered, release := make(chan struct{}), make(chan struct{})
	collect := func(context.Context) (map[string]any, error) {
		if n.Add(1) == 1 {
			close(entered)
			<-release
		}
		return map[string]any{"saved": true}, nil
	}
	rev := func(context.Context) (string, error) {
		if version.Load() == 1 {
			return "v1", nil
		}
		return "v2", nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, _, err := cache.read(ctx, rev, collect); first <- err }()
	<-entered
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled reader waited for projection")
	}
	// A mutation during collection is not mislabeled with the newer revision.
	version.Store(2)
	done := make(chan string, 20)
	for i := 0; i < 20; i++ {
		go func() {
			_, r, e := cache.read(t.Context(), rev, collect)
			if e != nil {
				done <- "error"
			} else {
				done <- r
			}
		}()
	}
	// All clients can be canceled without cancelling the shared collection.
	ctx2, cancel2 := context.WithCancel(t.Context())
	cancel2()
	if _, _, e := cache.read(ctx2, rev, collect); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	close(release)
	for i := 0; i < 20; i++ {
		r := <-done
		if r != "v1" && r != "v2" {
			t.Fatal(r)
		}
	}
	if n.Load() > 2 {
		t.Fatal("concurrent readers rebuilt", n.Load())
	}
	_, r, e := cache.read(t.Context(), rev, collect)
	if e != nil || r != "v2" || n.Load() != 2 {
		t.Fatal("revision invalidation", r, e, n.Load())
	}
}
