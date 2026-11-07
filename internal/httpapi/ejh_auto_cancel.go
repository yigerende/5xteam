package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/store"
)

type ejhAutoCancelState struct {
	LastAttempt *time.Time `json:"last_attempt,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Submitted   int        `json:"submitted"`
	Skipped     int        `json:"skipped"`
	Error       string     `json:"error"`
}

// Independent persisted clock; viewing/synchronizing the card page does not
// trigger cancellation. TryLock leaves in-flight issuer work undisturbed.
func (s *Server) tickEJHAutoCancel(ctx context.Context) {
	if !s.ejh.autoCancelMu.TryLock() {
		return
	}
	defer s.ejh.autoCancelMu.Unlock()
	if !s.tryBackgroundCardLock(&s.ejh.mu) {
		return
	}
	// Do not hold the issuer mutex while scanning local history or across
	// different cards. Provisioning/manual operations can run between checks.
	s.ejh.mu.Unlock()
	cfg, key, err := s.ejhConfig()
	if err != nil || !cfg.Enabled || !cfg.AutoCancelEnabled {
		return
	}
	var state ejhAutoCancelState
	if err = s.store.EJHGet("auto_cancel", "latest", &state); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return
	}
	interval := cfg.AutoCancelIntervalSeconds
	if interval == 0 {
		interval = 300
	}
	now := time.Now()
	if state.LastAttempt != nil && now.Before(state.LastAttempt.Add(time.Duration(interval)*time.Second)) {
		return
	}
	state = ejhAutoCancelState{LastAttempt: &now}
	if err = s.store.EJHPut("auto_cancel", "latest", state); err != nil {
		return
	}
	defer func() {
		finished := time.Now()
		state.FinishedAt = &finished
		_ = s.store.EJHPut("auto_cancel", "latest", state)
	}()
	cards, err := store.EJHRecords[ejh.CardRecord](s.store, "card")
	if err != nil {
		state.Error = err.Error()
		return
	}
	// Older refreshed cards go first; a failing network request cannot starve
	// every later card forever. Per-card check times persist across restarts.
	checks, err := store.EJHRecords[ejhAutoCancelCheck](s.store, "auto_cancel_check")
	if err != nil {
		state.Error = err.Error()
		return
	}
	checked := map[string]time.Time{}
	for _, check := range checks {
		checked[check.CardID] = check.At
	}
	sort.Slice(cards, func(i, j int) bool {
		a, b := checked[cards[i].ID], checked[cards[j].ID]
		if a.Equal(b) {
			return cards[i].ID < cards[j].ID
		}
		return a.Before(b)
	})
	job, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for _, card := range cards {
		if job.Err() != nil {
			state.Error = "本轮检查超时，剩余卡片下轮继续"
			return
		}
		if !ejh.AutoCancelNormalCard(card) {
			state.Skipped++
			continue
		}
		if err = s.store.EJHAutoCancelCheck(card.ID); err != nil {
			state.Skipped++
			_ = s.store.EJHPut("auto_cancel_check", card.ID, ejhAutoCancelCheck{CardID: card.ID, At: time.Now(), Reason: err.Error()})
			if !errors.Is(err, store.ErrEJHAutoCancelIneligible) {
				state.Error = err.Error()
			}
			continue
		}
		if err = s.store.EJHPut("auto_cancel_check", card.ID, ejhAutoCancelCheck{CardID: card.ID, At: time.Now()}); err != nil {
			state.Error = err.Error()
			return
		}
		if !s.tryBackgroundCardLock(&s.ejh.mu) {
			return
		}
		cardJob, done := context.WithTimeout(job, 30*time.Second)
		op, checkErr := s.autoCancelEJHCardLocked(cardJob, cfg, key, card)
		done()
		s.ejh.mu.Unlock()
		if op.ID != "" {
			state.Submitted++
		} else {
			state.Skipped++
		}
		if checkErr != nil {
			state.Error = fmt.Sprintf("卡片尾号 %s：%s", ejhAutoCancelLast4(card.Remote.Number), checkErr)
		}
		reason := "已提交销卡，进度见操作记录"
		if checkErr != nil {
			reason = checkErr.Error()
		}
		_ = s.store.EJHPut("auto_cancel_check", card.ID, ejhAutoCancelCheck{CardID: card.ID, At: time.Now(), Reason: reason})
	}
}

type ejhAutoCancelCheck struct {
	CardID string    `json:"card_id"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

func ejhAutoCancelLast4(number string) string {
	if len(number) > 4 {
		return number[len(number)-4:]
	}
	return "未知"
}

func (s *Server) autoCancelEJHCardLocked(ctx context.Context, cfg ejh.Settings, key string, card ejh.CardRecord) (ejh.Operation, error) {
	var empty ejh.Operation
	if err := s.store.EJHAutoCancelCheck(card.ID); err != nil {
		return empty, err
	}
	started := time.Now()
	var remote ejh.Card
	if err := s.ejhClient().Call(ctx, cfg, key, "card.query.info", map[string]any{"orderNo": card.Remote.OrderNo}, &remote); err != nil {
		return empty, err
	}
	if remote.OrderNo != card.Remote.OrderNo || remote.Number != card.Remote.Number {
		return empty, fmt.Errorf("卡片最新资料归属不一致")
	}
	if _, err := s.store.ImportEJHCard(remote, false); err != nil {
		return empty, err
	}
	if err := s.store.EJHGet("card", card.ID, &card); err != nil {
		return empty, err
	}
	if !ejh.AutoCancelNormalCard(card) {
		return empty, store.ErrEJHAutoCancelIneligible
	}
	start := card.CreatedAt
	if start.IsZero() {
		return empty, fmt.Errorf("卡片历史起始时间不明，无法核实完整流水")
	}
	// Import time may be later than the first local payment.
	cases, err := s.store.EJHCases()
	if err != nil {
		return empty, err
	}
	for _, c := range cases {
		if (c.CardID == card.ID || c.EJHCardID == card.ID) && !c.CreatedAt.IsZero() && c.CreatedAt.Before(start) {
			start = c.CreatedAt
		}
	}
	if err = s.collectEJHCardTrades(ctx, s.ejhClient(), cfg, key, card, start.Add(-24*time.Hour), time.Now()); err != nil {
		return empty, err
	}
	if err = s.store.EJHReconcile(); err != nil {
		return empty, err
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	op := ejh.Operation{ID: store.EJHID("auto-cancel", card.ID), Method: "cancel", CardID: card.ID, OrderNo: card.Remote.OrderNo,
		Currency: card.Remote.CardCurrency, Automatic: true, AutoCheckStartedAt: &started, Status: "submitting", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	// Atomically repeats ALL checks against current config, category, limit,
	// reservations and actual monetary evidence before claiming the operation.
	if err = s.store.EJHBeginOperation(op); err != nil {
		return empty, err
	}
	return s.submitEJHCancellationLocked(ctx, cfg, key, op)
}
