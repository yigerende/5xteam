package store

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestProMerchantReissueAtomicHistoryAndSnapshot(t *testing.T) {
	s := transferStore(t)
	email := "retry@example.com"
	transferSource(t, s, email)
	v := model.DefaultProSettings()
	v.MerchantSuccessFeeUSD = "1.25"
	if _, _, err := s.SaveProAutomationSettings(v, gptpay.Settings{PlanCode: "pro5"}, "", "test-key"); err != nil {
		t.Fatal(err)
	}
	p, sub, err := s.PrepareProMerchant(email)
	if err != nil {
		t.Fatal(err)
	}
	id := p.ProMerchant.RequestID
	now := time.Now().Add(-time.Minute)
	p, err = s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProMerchant.JobID, p.ProMerchant.Status, p.ProMerchant.RemoteStatus = "failed-job", "failed", "failed"
		p.ProMerchant.AcceptedAt, p.ProMerchant.RetiredAt = &now, &now
		p.ProAuto = model.ProAutoState{ID: "original-flow", OrderID: "paid-order", Steps: map[string]string{"recharge": "completed", "oauth": "completed", "merchant": "failed"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	// A stop while the query is in flight wins over that stale retry.
	if _, err = s.ReissueProMerchant(email, id, p.ProAuto.StopVersion+1); err == nil {
		t.Fatal("ignored stop version")
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ReissueProMerchant(email, id, 0); err == nil {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("created %d new attempts", won.Load())
	}
	current, kept, err := s.PrepareProMerchant(email)
	if err != nil {
		t.Fatal(err)
	}
	m := current.ProMerchant
	if m.RequestID == id || m.JobID != "" || m.Attempts != 0 || m.AcceptedAt != nil || m.CompletedAt != nil || m.RetiredAt == nil || !m.RetiredAt.Equal(now) || m.SuccessFeeUSD != "1.25" {
		t.Fatal("incorrect reset", m)
	}
	if current.ProAuto.ID != "original-flow" || current.ProAuto.OrderID != "paid-order" || current.ProAuto.Steps["recharge"] != "completed" || current.ProAuto.Steps["oauth"] != "completed" || current.ProAuto.Steps["merchant"] != "waiting" {
		t.Fatal("changed payment flow")
	}
	if string(sub.Payload) != string(kept.Payload) || sub.APIKey != kept.APIKey || sub.APIURL != kept.APIURL {
		t.Fatal("changed pinned submission")
	}
	var raw string
	if err = s.db.QueryRow("SELECT payload FROM auto_rotation_events WHERE id=?", "merchant-reissue:"+p.ID+":"+id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var event model.AutoRotationEvent
	if json.Unmarshal([]byte(raw), &event) != nil || !strings.Contains(raw, "failed-job") || event.Details["request_id"] != m.RequestID {
		t.Fatal("missing old attempt history")
	}
	for _, secret := range []string{"test-key", "gpt-pw", "saved-at", "saved-rt"} {
		if strings.Contains(raw, secret) {
			t.Fatal("history leaked credentials")
		}
	}
	if _, err = s.ReissueProMerchant(email, id, 0); err == nil {
		t.Fatal("stale duplicate rotated request again")
	}
}

func TestProMerchantReissueRejectsNonterminalAndCharged(t *testing.T) {
	s := transferStore(t)
	email := "blocked@example.com"
	transferSource(t, s, email)
	if _, _, err := s.SaveProAutomationSettings(model.DefaultProSettings(), gptpay.Settings{PlanCode: "pro5"}, "", "test-key"); err != nil {
		t.Fatal(err)
	}
	p, _, err := s.PrepareProMerchant(email)
	if err != nil {
		t.Fatal(err)
	}
	id := p.ProMerchant.RequestID
	for _, status := range []string{"", "queued", "processing", "verifying", "attention", "succeeded", "banned"} {
		_, err = s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			p.ProMerchant.JobID = "job"
			p.ProMerchant.Status = "failed"
			p.ProMerchant.RemoteStatus = status
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ReissueProMerchant(email, id, 0); err == nil {
			t.Fatal("allowed", status)
		}
	}
	_, err = s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProMerchant.RemoteStatus = "failed"; p.ProMerchant.JobID = "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReissueProMerchant(email, id, 0); err == nil {
		t.Fatal("reissued unknown submission")
	}
	_, err = s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProMerchant.JobID = "job"
		now := time.Now()
		p.ProMerchant.CompletedAt = &now
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProMerchant.CompletedAt = nil; p.ProMerchant.Status = "failed" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReissueProMerchant(email, id, 0); err == nil {
		t.Fatal("reissued previously charged request")
	}
}
