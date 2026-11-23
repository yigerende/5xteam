package store

import (
	"chatgpt-space-merge/internal/model"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProSIWCImportRecoveryAtomicAndDurable(t *testing.T) {
	s := transferStore(t)
	email := "recover@example.com"
	transferSource(t, s, email)
	a := model.ProAutoState{ID: "flow", OrderID: "paid", Status: "running", Stage: "oauth", Steps: map[string]string{"recharge": "completed", "oauth": "running"}, RetryAttempts: 3, ProxyRetryAttempts: 2, RestockBinding: "original-pool", StartedAt: time.Now()}
	o := transferOrder(email, a.OrderID, "success")
	if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto = a; p.ProRestockTaskID = "original-task" }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sub2ProxyUsages("site", map[string]int64{"proxy": 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveSub2ProxyBinding("pending", "encrypted callback", "site", "proxy", "key"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSub2PushAttempt("other", "keep"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var wins atomic.Int32
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PrepareProSIWCImportRecovery(email, a, "pending", "encrypted callback", "key"); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claims=%d", wins.Load())
	}
	p, _, _ := s.MailAccountCredential(email)
	if p.ProAuto.SIWCImportRecoveries != 1 || p.ProAuto.OrderID != a.OrderID || p.ProAuto.RetryAttempts != 3 || p.ProAuto.ProxyRetryAttempts != 2 || p.ProRestockTaskID != "original-task" || p.ProAuto.RestockBinding != "original-pool" {
		t.Fatal("workflow changed", p.ProAuto)
	}
	if raw, _ := s.Sub2PushAttempt("pending"); raw != "" {
		t.Fatal("old callback retained")
	}
	if raw, _ := s.Sub2PushAttempt("other"); raw != "keep" {
		t.Fatal("other request removed")
	}
	u, _ := s.Sub2ProxyUsages("site", nil)
	if u["proxy"].Count != 2 || u["proxy"].Pending != 0 {
		t.Fatal("reservation not released", u)
	}
	if err := s.RecoverProAutomations(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.Status = "running" }); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSub2PushAttempt("pending", "new callback"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareProSIWCImportRecovery(email, a, "pending", "new callback", "new-key"); err == nil {
		t.Fatal("restart granted another automatic recovery")
	}
	order, _, _ := s.GPTPayOrder(a.OrderID)
	if order.Status != "success" {
		t.Fatal("paid order changed")
	}
}

func TestProSIWCImportRecoveryGuards(t *testing.T) {
	for _, kind := range []string{"stopped", "flow", "stop_version", "unpaid", "pending_changed", "already_bound", "completed"} {
		t.Run(kind, func(t *testing.T) {
			s := transferStore(t)
			email := "guard@example.com"
			transferSource(t, s, email)
			a := model.ProAutoState{ID: "flow", OrderID: "paid", Status: "running", Stage: "oauth", Steps: map[string]string{"recharge": "completed", "oauth": "running"}}
			o := transferOrder(email, a.OrderID, "success")
			if kind == "unpaid" {
				o.Order.Status = "processing"
			}
			if err := s.CreateGPTPayOrder(o.Order, o.Snapshot); err != nil {
				t.Fatal(err)
			}
			s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
				p.ProAuto = a
				if kind == "stopped" {
					p.ProAuto.StopRequested = true
				}
				if kind == "completed" {
					p.Sub2AccountID = 88
				}
			})
			s.SaveSub2PushAttempt("pending", "old")
			if kind == "flow" {
				a.ID = "stale"
			}
			if kind == "stop_version" {
				a.StopVersion++
			}
			expected := "old"
			if kind == "pending_changed" {
				expected = "stale"
			}
			if kind == "already_bound" {
				s.Sub2ProxyUsages("site", map[string]int64{"proxy": 1})
				s.ReserveSub2ProxyBinding("pending", "old", "site", "proxy", "key")
				s.CompleteSub2ProxyBinding("pending", "old", "key", 88)
			}
			if _, err := s.PrepareProSIWCImportRecovery(email, a, "pending", expected, "key"); err == nil {
				t.Fatal("guard bypassed")
			}
			if raw, _ := s.Sub2PushAttempt("pending"); raw != "old" {
				t.Fatal("failed guard cleared callback")
			}
		})
	}
}
