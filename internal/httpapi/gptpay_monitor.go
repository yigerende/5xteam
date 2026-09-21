package httpapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"chatgpt-space-merge/internal/gptpay"
)

var errGPTPayRemoteNotFound = errors.New("供应商未找到该订单，请核对供应商账户；原订单已保留")

func (s *Server) startGPTPayOrderMonitor(parent context.Context) {
	s.gptpayMonitorMu.Lock()
	defer s.gptpayMonitorMu.Unlock()
	if s.gptpayMonitorClosed || s.gptpayMonitorCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.gptpayMonitorCancel = cancel
	s.gptpayMonitorWG.Add(1)
	go func() {
		defer s.gptpayMonitorWG.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			s.pollGPTPayOrders(ctx, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Server) stopGPTPayOrderMonitor() {
	s.gptpayMonitorMu.Lock()
	s.gptpayMonitorClosed = true
	if s.gptpayMonitorCancel != nil {
		s.gptpayMonitorCancel()
	}
	s.gptpayMonitorMu.Unlock()
	s.gptpayMonitorWG.Wait()
}

func (s *Server) pollGPTPayOrders(ctx context.Context, now time.Time) {
	if ctx.Err() != nil || !s.gptpayPollMu.TryLock() {
		return
	}
	defer s.gptpayPollMu.Unlock()
	orders, err := s.store.GPTPayOrdersDue(now, 20)
	if err != nil {
		return
	}
	// Two bounded workers; each sweep finishes before another starts.
	jobs := make(chan gptpay.Order, len(orders))
	for _, o := range orders {
		jobs <- o
	}
	close(jobs)
	var wg sync.WaitGroup
	for i := 0; i < min(2, len(orders)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for o := range jobs {
				if ctx.Err() != nil {
					return
				}
				s.pollGPTPayOrder(ctx, o, now)
			}
		}()
	}
	wg.Wait()
}

func (s *Server) pollGPTPayOrder(ctx context.Context, o gptpay.Order, now time.Time) {
	v, _ := s.proLocks.LoadOrStore("account:"+o.Email, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	if !lock.TryLock() {
		return
	}
	defer lock.Unlock()
	if s.proAutomationRunning(o.Email) {
		return
	}
	o, snapshot, err := s.store.GPTPayOrder(o.ID)
	if err != nil || !o.NeedsStatusPoll() || (o.NextStatusCheckAt != nil && o.NextStatusCheckAt.After(now)) {
		return
	}
	queryCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	_, _ = s.syncGPTPayOrderStatus(queryCtx, o, snapshot)
}

// Shared by foreground refresh and background polling under the account lock.
// Cancellation is separate from purchase success and cannot hold up Pro steps.
func (s *Server) syncGPTPayOrderStatus(ctx context.Context, o gptpay.Order, snapshot gptpay.Snapshot) (gptpay.Order, error) {
	before := o
	items, err := s.gptPayClient().Status(ctx, snapshot.URL, snapshot.APIKey, []string{o.Remote.ID})
	if err == nil {
		found := false
		for _, remote := range items {
			if remote.ID != o.Remote.ID {
				continue
			}
			found = true
			if remote.Status == "not_found" {
				err = errGPTPayRemoteNotFound
				break
			}
			if !gptpay.ValidStatus(remote.Status) {
				err = errors.New("供应商返回未知订单状态，原订单已保留")
				break
			}
			if !o.Active() && remote.Status != o.Status {
				break
			}
			remote.FailureReason = gptpay.Redact(remote.FailureReason, snapshot)
			o.Remote, o.Status, o.Error = remote, remote.Status, remote.FailureReason
			break
		}
		if !found {
			err = errors.New("供应商未返回该订单，原订单已保留")
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return before, ctx.Err()
	}
	now := time.Now().UTC()
	o.UpdatedAt = now
	o.LastStatusCheckAt, o.NextStatusCheckAt, o.StatusCheckError = &now, nil, ""
	if err != nil {
		o.StatusCheckError = gptpay.Redact(err.Error(), snapshot)
	}
	if o.NeedsStatusPoll() {
		delay := 15 * time.Second
		if now.Sub(o.CreatedAt) < time.Minute {
			delay = 5 * time.Second
		}
		if err != nil || !o.Active() {
			delay = time.Minute
		}
		next := now.Add(delay)
		o.NextStatusCheckAt = &next
	}
	if saveErr := s.store.UpdateGPTPayOrder(o); saveErr != nil {
		return before, fmt.Errorf("保存订单状态失败：%w", saveErr)
	}
	if err == nil {
		if before.Remote != o.Remote || before.Status != o.Status || before.StatusCheckError != "" {
			s.auditGPTPayOrder(o)
		} else {
			s.updateProPaymentStage(o)
		}
	} else if before.StatusCheckError != o.StatusCheckError {
		if p, _, e := s.store.MailAccountCredential(o.Email); e == nil {
			s.auditProEvent(p, "recharge", "running", "gptpay", "订单状态查询失败，保留原订单，后台稍后重查", map[string]any{"order_id": o.ID, "error": o.StatusCheckError})
		}
	}
	return o, err
}
