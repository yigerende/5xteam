package httpapi

import (
	"context"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/ejh"
)

// Caller already persisted EJHBeginOperation. Never retry this mutation: a
// timeout retains its claim and the existing query-only monitor takes over.
func (s *Server) submitEJHCancellationLocked(ctx context.Context, cfg ejh.Settings, key string, op ejh.Operation) (ejh.Operation, error) {
	var out struct {
		OrderNo string `json:"orderNo"`
		Status  string `json:"status"`
	}
	err := s.ejhClient().Call(ctx, cfg, key, "card.cancel.info", map[string]any{"orderNo": op.OrderNo}, &out)
	op.Status = "unknown"
	if err != nil {
		op.Error = err.Error()
	} else if out.OrderNo != op.OrderNo || out.Status != "SUCCESS" {
		op.Error = "返回状态或订单号不匹配，请核对"
	} else {
		op.Status, op.Note = "processing", "销卡申请已提交，正在查询卡台处理结果"
	}
	op.UpdatedAt = time.Now()
	if err = s.store.EJHPut("operation", op.ID, op); err != nil {
		return op, err
	}
	job, done := context.WithTimeout(ctx, 12*time.Second)
	defer done()
	return s.checkEJHCancellationLocked(job, cfg, key, op)
}

// Query only: never replay card.cancel.info, including after timeouts/restarts.
func (s *Server) checkEJHCancellationLocked(ctx context.Context, cfg ejh.Settings, key string, op ejh.Operation) (ejh.Operation, error) {
	var remote ejh.Card
	err := s.ejhClient().Call(ctx, cfg, key, "card.query.info", map[string]string{"orderNo": op.OrderNo}, &remote)
	if err == nil && remote.OrderNo != op.OrderNo {
		err = fmt.Errorf("卡台返回订单号不一致，保留原销卡状态")
	}
	if err == nil {
		_, err = s.store.ImportEJHCard(remote, false)
	}
	var saved ejh.Operation
	if readErr := s.store.EJHGet("operation", op.ID, &saved); readErr != nil {
		return op, readErr
	}
	if saved.Status == "completed" || saved.Status == "failed" || saved.Status == "resolved" {
		return saved, nil
	}
	saved.CheckError = ""
	if err != nil {
		saved.CheckError = err.Error()
	}
	next := time.Now().Add(5 * time.Second)
	if saved.Status == "unknown" && time.Since(saved.CreatedAt) > 5*time.Minute {
		next = time.Now().Add(time.Minute)
	}
	saved.NextCheckAt, saved.UpdatedAt = &next, time.Now()
	if err = s.store.EJHPut("operation", saved.ID, saved); err != nil {
		return saved, err
	}
	return saved, nil
}

func (s *Server) tickEJHCancellations(ctx context.Context) {
	if !s.tryMaintenanceCardLock(&s.ejh.mu) {
		return
	}
	defer s.ejh.mu.Unlock()
	cfg, key, err := s.ejhConfig()
	if err != nil {
		return
	}
	ops, err := s.store.EJHPendingCancellations(time.Now(), 10)
	if err != nil || len(ops) == 0 {
		return
	}
	job, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for _, op := range ops {
		if job.Err() != nil || s.manualCardPreparationPending() || s.proCardPreparing.Load() > 0 {
			return
		}
		_, _ = s.checkEJHCancellationLocked(job, cfg, key, op)
	}
}
