package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"context"
	"database/sql"
	"errors"
)

// The full automation continues after delivery (quota, merge, removal). A
// consumed account must not turn back into incoming supply just because it is
// no longer schedulable or has moved out of the monitored group.
func restockAccountExiting(p model.MailAccountProfile) bool {
	if p.PushProvider != "sub2" || p.Sub2AccountID <= 0 || (p.PushStatus != "completed" && p.PushStatus != "removed") {
		return false
	}
	if p.ProAuto.ID != "" {
		// Old push/merge flags cannot finish a new provisioning attempt.
		if p.ProAuto.Steps["push"] != "completed" {
			return false
		}
		switch p.ProAuto.Stage {
		case "login", "recharge", "oauth", "push":
			return false
		}
	}
	return p.ProWorkflowRunning || p.ProAuto.Stage == "merge" || p.ProAuto.Stage == "deactivate" || p.DispositionStarted() || p.SpaceMergedOnce ||
		p.ProAuto.Steps["merge"] == "completed" || p.RemoveStatus == "completed" || p.RemoveStatus == "team_removed"
}

// Only delivery tracking is finished. Payment uncertainty keeps its original
// order, budget and inflight occupancy even if progress was manually edited.
func (s *Server) restockExitSettled(p model.MailAccountProfile, orderID string) (bool, error) {
	if !restockAccountExiting(p) {
		return false, nil
	}
	if _, err := s.store.ActiveGPTPayOrderState(p.Email); err == nil {
		return false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if orderID == "" {
		orderID = p.ProAuto.OrderID
	}
	linked := orderID != ""
	if orderID == "" && p.ProAuto.ID != "" {
		orderID = p.ProAuto.PaymentOrderID()
	}
	if orderID == "" {
		return true, nil // Historical/manual delivery without a payment link.
	}
	order, err := s.store.GPTPayOrderState(orderID)
	if errors.Is(err, sql.ErrNoRows) {
		return !linked, nil
	}
	if err != nil {
		return false, err
	}
	return order.Status == "success", nil
}

// History is read in one independent WAL snapshot. Repeated per-account reads
// on the shared store mutex can consume the entire monitoring freshness window.
// Return only candidates; the caller rechecks their live payment/workflow state
// before applying the original observation and reservation rules.
func (s *Server) restockExternalCandidates(ctx context.Context, seen map[string]bool) ([]model.MailAccountProfile, error) {
	reader, err := s.store.ProListReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	profiles, err := reader.ProRestockExternalProfiles(ctx)
	if err != nil {
		return nil, err
	}
	view := &Server{store: reader}
	candidates := make([]model.MailAccountProfile, 0)
	for _, profile := range profiles {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if seen[profile.Email] || profile.ProRestockTaskID != "" {
			continue
		}
		settled, err := view.restockExitSettled(profile, "")
		if err != nil {
			return nil, err
		}
		if settled {
			continue
		}
		if profile.ProAuto.ID == "" && !restockManualWork(profile) {
			if _, err = reader.ActiveGPTPayOrderState(profile.Email); errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				return nil, err
			}
		}
		candidates = append(candidates, profile)
	}
	return candidates, nil
}

func restockManualWork(p model.MailAccountProfile) bool {
	for _, stage := range p.ProManualStages {
		if stage.Status == "running" {
			return true
		}
	}
	return p.PushStatus != "completed" && p.ProManualStages["recharge"].Status == "completed"
}
