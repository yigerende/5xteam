package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/refundmerchant"
)

func TestProMerchantReissueChecksOriginalAndSubmitsOnce(t *testing.T) {
	f := merchantFixture(t)
	p, _, err := f.s.store.PrepareProMerchant(f.email)
	if err != nil {
		t.Fatal(err)
	}
	id := p.ProMerchant.RequestID
	_, err = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProMerchant.JobID = "old-job"
		p.ProMerchant.Status = "failed"
		p.ProMerchant.RemoteStatus = "failed"
		now := time.Now()
		p.ProMerchant.AcceptedAt = &now
		p.ProMerchant.RetiredAt = &now
	})
	if err != nil {
		t.Fatal(err)
	}
	var gets, posts atomic.Int32
	var newKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gets.Add(1)
			fmt.Fprintf(w, `{"id":"old-job","email":%q,"status":"failed"}`, f.email)
			return
		}
		n := posts.Add(1)
		if n > 1 && newKey != r.Header.Get("Idempotency-Key") {
			t.Error("lost response changed the new idempotency key")
		}
		newKey = r.Header.Get("Idempotency-Key")
		if n == 1 {
			w.WriteHeader(502)
			return
		}
		fmt.Fprint(w, `{"ids":["new-job"]}`)
	}))
	defer srv.Close()
	f.s.proMerchantClient = &refundmerchant.Client{BaseURL: srv.URL}
	body := fmt.Sprintf(`{"request_id":%q}`, id)
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := f.request(f.s.reissueProMerchant, "POST", "/merchant-retry", body)
			if w.Code == 202 {
				accepted.Add(1)
			} else if w.Code != 409 {
				t.Errorf("unexpected response %d %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	f.s.proMergeWG.Wait()
	p, _, err = f.s.store.MailAccountCredential(f.email)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Load() != 1 || gets.Load() != 1 || posts.Load() != 1 || newKey == id || newKey != p.ProMerchant.RequestID || p.ProMerchant.JobID != "" || p.MerchantCompleted() {
		t.Fatal("incorrect submission", accepted.Load(), gets.Load(), posts.Load(), p.ProMerchant)
	}
	p, err = f.s.performProMerchant(t.Context(), f.email)
	if err != nil || posts.Load() != 2 || p.ProMerchant.RequestID != newKey || p.ProMerchant.JobID != "new-job" {
		t.Fatal("lost response did not resume the same new order", err)
	}
	if w := f.request(f.s.reissueProMerchant, "POST", "/merchant-retry", body); w.Code != 409 {
		t.Fatal("replayed stale button")
	}
	if len(f.calls) != 0 {
		t.Fatal("reran local upstream workflow")
	}
}

func TestProMerchantReissueDoesNotTrustStaleFailure(t *testing.T) {
	f := merchantFixture(t)
	p, _, err := f.s.store.PrepareProMerchant(f.email)
	if err != nil {
		t.Fatal(err)
	}
	id := p.ProMerchant.RequestID
	_, err = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProMerchant.JobID = "old-job"
		p.ProMerchant.Status = "failed"
		p.ProMerchant.RemoteStatus = "failed"
	})
	if err != nil {
		t.Fatal(err)
	}
	status := "processing"
	httpStatus := 200
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			posts++
			t.Error("submitted despite unconfirmed failure")
		}
		w.WriteHeader(httpStatus)
		fmt.Fprintf(w, `{"id":"old-job","email":%q,"status":%q}`, f.email, status)
	}))
	defer srv.Close()
	f.s.proMerchantClient = &refundmerchant.Client{BaseURL: srv.URL}
	body := fmt.Sprintf(`{"request_id":%q}`, id)
	for _, next := range []string{"queued", "processing", "verifying", "attention", "succeeded", "banned", "unknown"} {
		status = next
		if w := f.request(f.s.reissueProMerchant, "POST", "/merchant-retry", body); w.Code != 409 {
			t.Fatal("accepted", next, w.Code)
		}
	}
	httpStatus = 502
	if w := f.request(f.s.reissueProMerchant, "POST", "/merchant-retry", body); w.Code != 502 {
		t.Fatal("ignored query failure")
	}
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if p.ProMerchant.RequestID != id || posts != 0 {
		t.Fatal("changed request on query failure")
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "fixture-password") {
		t.Fatal("leaked secret")
	}
}

func TestProMerchantReissueStopDuringVerification(t *testing.T) {
	f := merchantFixture(t)
	p, _, err := f.s.store.PrepareProMerchant(f.email)
	if err != nil {
		t.Fatal(err)
	}
	id := p.ProMerchant.RequestID
	if _, err = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProMerchant.JobID = "old-job"
		p.ProMerchant.Status = "failed"
		p.ProMerchant.RemoteStatus = "failed"
	}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected new order")
		}
		close(entered)
		<-release
		fmt.Fprintf(w, `{"id":"old-job","email":%q,"status":"failed"}`, f.email)
	}))
	defer srv.Close()
	f.s.proMerchantClient = &refundmerchant.Client{BaseURL: srv.URL}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.request(f.s.reissueProMerchant, "POST", "/merchant-retry", fmt.Sprintf(`{"request_id":%q}`, id))
	}()
	<-entered
	stopped := f.request(f.s.stopProAutomation, "POST", "/stop", "{}")
	if stopped.Code != 200 {
		t.Errorf("stop failed %s", stopped.Body.String())
	}
	close(release)
	if w := <-done; w.Code != 409 {
		t.Fatal("retry defeated Stop", w.Code, w.Body.String())
	}
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if !p.ProAuto.StopRequested || p.ProMerchant.RequestID != id {
		t.Fatal("lost stop or changed request")
	}
}
