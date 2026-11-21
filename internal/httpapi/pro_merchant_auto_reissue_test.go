package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
)

func TestStarBridgeMerchantAutoReissueOnce(t *testing.T) {
	for _, final := range []string{"succeeded", "failed"} {
		t.Run(final, func(t *testing.T) {
			f := merchantFixture(t)
			var keys, bodies []string
			var requests int
			status := "failed"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer bridge-key" {
					t.Error("changed pinned credentials")
				}
				if r.Method == http.MethodPost {
					raw, _ := io.ReadAll(r.Body)
					keys = append(keys, r.Header.Get("Idempotency-Key"))
					bodies = append(bodies, string(raw))
					if len(keys) == 2 {
						w.WriteHeader(502) // New order accepted, response lost.
						return
					}
					fmt.Fprintf(w, `{"ids":["job-%d"]}`, len(keys))
					return
				}
				requests++
				id := strings.TrimPrefix(r.URL.Path, "/api/v1/refund/orders/")
				// A business failure alone is sufficient, regardless of its message.
				fmt.Fprintf(w, `{"order":{"id":%q,"email":%q,"status":%q,"message":"any error"}}`, id, f.email, status)
			}))
			defer srv.Close()
			cfg, _, _, _ := f.s.store.ProSettings()
			cfg.MerchantProvider, cfg.MerchantStarBridgeAPIURL = "starbridge", srv.URL+"/api/v1/refund/orders"
			if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, "", "", "bridge-key"); err != nil {
				t.Fatal(err)
			}
			if err := f.s.runProAutomationTail(t.Context(), f.email); err != nil {
				t.Fatal(err)
			}
			first, _, _ := f.s.store.MailAccountCredential(f.email)
			if !first.ProMerchant.AutoReissueEnabled {
				t.Fatal("automatic tail did not pin reissue allowance")
			}
			// Changing the selected supplier must not redirect the second order.
			cfg.MerchantProvider = refundmerchant.ProviderOriginal
			if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, "", "original-key", "changed-key"); err != nil {
				t.Fatal(err)
			}
			p, err := f.s.performProMerchant(t.Context(), f.email)
			if err != nil || p.ProMerchant.RequestID == first.ProMerchant.RequestID || p.ProMerchant.AutoReissues != 1 || p.ProAuto.Status != "awaiting_merchant" || p.ProMerchant.NextCheckAt == nil {
				t.Fatal("failed order did not become a queued new request", p.ProMerchant, err)
			}
			if p.ProAuto.ID != first.ProAuto.ID || p.ProAuto.OrderID != first.ProAuto.OrderID || p.ProAuto.RetryAttempts != first.ProAuto.RetryAttempts || !p.ProMerchant.RetiredAt.Equal(*first.ProMerchant.RetiredAt) {
				t.Fatal("reissue changed provisioning or downstream state")
			}
			if err = f.s.store.RecoverProWorkflows(); err != nil {
				t.Fatal(err)
			}
			due, err := f.s.store.DueProMerchants(time.Now().Add(time.Minute))
			if err != nil || len(due) != 1 || due[0].ProMerchant.RequestID != p.ProMerchant.RequestID {
				t.Fatal("restart stranded new order", err)
			}
			if _, err = f.s.performProMerchant(t.Context(), f.email); err == nil {
				t.Fatal("expected lost response")
			}
			if _, err = f.s.performProMerchant(t.Context(), f.email); err != nil {
				t.Fatal(err)
			}
			status = final
			for i := 0; i < 2; i++ {
				p, err = f.s.performProMerchant(t.Context(), f.email)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(keys) != 3 || keys[0] == keys[1] || keys[1] != keys[2] || bodies[0] != bodies[1] || bodies[1] != bodies[2] || p.ProMerchant.AutoReissues != 1 || p.ProMerchant.NextCheckAt != nil {
				t.Fatal("wrong order identities, payload or retry limit", keys, p.ProMerchant)
			}
			if p.MerchantCompleted() != (final == "succeeded") || len(f.calls) != 0 || requests < 2 {
				t.Fatal("incorrect final state or reran provisioning")
			}
			fees, err := f.s.store.ProMerchantFees("")
			wantFees := 0
			if final == "succeeded" {
				wantFees = 1
			}
			if err != nil || len(fees) != wantFees {
				t.Fatal("incorrect success fee count", len(fees), err)
			}
			events, total, err := f.s.store.AutoRotationEventsPage("", "", p.ID, "pro_merchant_retry", "", 10, 0)
			if err != nil || total != 1 || events[0].Details["automatic"] != true || !strings.Contains(fmt.Sprint(events[0].Details["previous_merchant"]), "job-1") {
				t.Fatal("missing failed order audit", total, err)
			}
		})
	}
}

func TestStarBridgeMerchantAutoReissueRequiresFailed(t *testing.T) {
	f := merchantFixture(t)
	status, httpStatus := "queued", 200
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			fmt.Fprint(w, `{"ids":["job"]}`)
			return
		}
		w.WriteHeader(httpStatus)
		fmt.Fprintf(w, `{"order":{"id":"job","email":%q,"status":%q}}`, f.email, status)
	}))
	defer srv.Close()
	cfg, _, _, _ := f.s.store.ProSettings()
	cfg.MerchantProvider, cfg.MerchantStarBridgeAPIURL = "starbridge", srv.URL+"/api/v1/refund/orders"
	if _, _, err := f.s.store.SaveProAutomationSettings(cfg, gptpay.Settings{PlanCode: "pro5"}, "", "", "bridge-key"); err != nil {
		t.Fatal(err)
	}
	// Exercise the scheduler's automatic entry point, not just the inline tail.
	if _, err := f.s.startProDispositionJob(f.email, nil, nil); err != nil {
		t.Fatal(err)
	}
	f.s.proMergeWG.Wait()
	first, _, _ := f.s.store.MailAccountCredential(f.email)
	if !first.ProMerchant.AutoReissueEnabled {
		t.Fatal("scheduled automatic tail lost its origin")
	}
	for _, next := range []string{"queued", "processing", "verifying", "attention", "unknown", "cancelled", "banned"} {
		status = next
		p, err := f.s.performProMerchant(t.Context(), f.email)
		if err != nil || p.ProMerchant.RequestID != first.ProMerchant.RequestID || p.ProMerchant.AutoReissues != 0 || posts != 1 {
			t.Fatal("reissued for non-failed status", next, err)
		}
	}
	httpStatus = 502
	status = "failed"
	p, err := f.s.performProMerchant(t.Context(), f.email)
	if err == nil || p.ProMerchant.RequestID != first.ProMerchant.RequestID || p.ProMerchant.AutoReissues != 0 {
		t.Fatal("query HTTP error created a new order")
	}
	// Stop received during a pending query must take priority over reissue.
	httpStatus = 200
	_, err = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = true })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.performProMerchant(t.Context(), f.email); err == nil {
		t.Fatal("ignored operator stop")
	}
}
