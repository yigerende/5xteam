package httpapi

import (
	"context"
	"fmt"
	"strings"

	"chatgpt-space-merge/internal/nexovcc"
	"chatgpt-space-merge/internal/store"
)

type nexovccDeferCardDetailsKey struct{}

func nexovccNeedsCardDetails(card nexovcc.Card) bool {
	return !card.Imported || (card.Error != "" && !strings.HasPrefix(card.Error, "余额与流水同步失败：") && !strings.HasPrefix(card.Error, "余额同步失败："))
}

func nexovccWaitingReturnCards(cases []store.NexovccCase) map[string]bool {
	waiting := map[string]bool{}
	for _, c := range cases {
		if c.PaymentStatus == "success" && c.State == "waiting" && c.RemovedAt == "" &&
			(c.Match == nil || !c.Match.Group.Valid || !c.Match.Group.FullyReturned) {
			waiting[c.CardID] = true
		}
	}
	return waiting
}

// Stage one card's balance, trades and funds in memory. No partial financial
// data is visible to either the UI or automatic admission decisions.
func (s *Server) syncNexovccCardSnapshotLocked(ctx context.Context, id string) (err error) {
	defer func() {
		if err != nil && ctx.Err() == nil {
			_ = s.store.SetNexovccCardSyncError(id, "余额与流水同步失败："+err.Error())
		}
	}()
	card, err := s.store.NexovccCard(id)
	if err != nil {
		return err
	}
	deferred, _ := ctx.Value(nexovccDeferCardDetailsKey{}).(bool)
	if deferred && card.Status != "4" && nexovccNeedsCardDetails(card) {
		card, err = s.refreshNexovccDetailsLocked(ctx, id, card.Serial, false)
		if err != nil {
			return err
		}
	}
	number, err := s.nexovccFinanceNumberLocked(card)
	if err != nil {
		return err
	}
	card, err = s.readNexovccCardBalanceLocked(ctx, card)
	if err != nil {
		return fmt.Errorf("读取余额：%w", err)
	}
	client, _, err := s.nexovccClientLocked()
	if err != nil {
		return err
	}
	fresh, err := client.ReadCardFinance(ctx, number, card.Serial)
	if err != nil {
		return fmt.Errorf("读取交易与资金流水：%w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return s.store.SaveNexovccCardSnapshot(card, fresh)
}

// A scheduled scan can evaluate the just-committed card immediately. Never
// block acquiring admission while holding the issuer mutex: if Pro owns it,
// leave the independent monitor to handle this card on its next tick.
func (s *Server) afterNexovccCardSnapshotLocked(ctx context.Context, id string) error {
	scheduled, _ := ctx.Value(nexovccScheduledSyncKey{}).(bool)
	if !scheduled || ctx.Err() != nil || !s.tryBackgroundCardLock(&s.proCardAdmissionMu) {
		return ctx.Err()
	}
	defer s.proCardAdmissionMu.Unlock()
	if err := s.checkNexovccControl(); err != nil {
		return err
	}
	// The issuer lock stays held. Hide the read-only cancellation handle while
	// submitting money operations; stop-all still cancels the parent context.
	s.nexovcc.jobMu.Lock()
	readCancel, readOnly := s.nexovcc.readCancel, s.nexovcc.job.ReadOnly
	s.nexovcc.readCancel, s.nexovcc.job.ReadOnly = nil, false
	s.nexovcc.jobMu.Unlock()
	defer func() {
		s.nexovcc.jobMu.Lock()
		s.nexovcc.readCancel, s.nexovcc.job.ReadOnly = readCancel, readOnly
		s.nexovcc.jobMu.Unlock()
	}()
	withdrawErr := s.autoWithdrawNexovccLocked(ctx, id)
	// An uncertain withdrawal must be resolved before considering closure.
	if withdrawErr != nil {
		return withdrawErr
	}
	return s.autoRecycleNexovccLocked(ctx, id)
}
