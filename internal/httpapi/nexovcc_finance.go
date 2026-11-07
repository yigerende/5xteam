package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"chatgpt-space-merge/internal/nexovcc"
)

// Routine refreshes query only the cards shown in the normal tab.
// Pending closing operations reconcile their own receipts separately.
func (s *Server) syncNexovccFinanceLocked(ctx context.Context, forceAccount ...bool) error {
	return s.syncNexovccFinanceScopeLocked(ctx, "", true, len(forceAccount) > 0 && forceAccount[0], "normal")
}

// An empty card ID selects normal cards; an explicit ID can refresh closed
// history too. These actions never expand into account-wide ledger requests.
func (s *Server) syncNexovccTransactionsLocked(ctx context.Context, cardID string) error {
	return s.syncNexovccFinanceScopeLocked(ctx, cardID, false, false, "normal")
}

func (s *Server) syncNexovccFinanceScopeLocked(ctx context.Context, cardID string, account, forceAccount bool, category string) (err error) {
	defer func() {
		if ctx.Err() != nil || !account {
			return
		}
		message := ""
		if err != nil {
			message = err.Error()
		}
		_ = s.store.NexovccCache("finance_error", message)
	}()
	_, cfg, err := s.nexovccClientLocked()
	if err != nil {
		return err
	}
	var f nexovcc.Finance
	if err = s.store.NexovccCached("finance", &f); err != nil {
		return err
	}
	cards, err := s.store.NexovccCards()
	if err != nil {
		return err
	}
	if f.CardAt == nil {
		f.CardAt = map[string]time.Time{}
		for _, card := range cards {
			if !f.At.IsZero() {
				f.CardAt[card.Serial] = f.At
			}
		}
	}
	scheduled, _ := ctx.Value(nexovccScheduledSyncKey{}).(bool)
	var ops []nexovcc.Operation
	if scheduled {
		ops, err = s.store.NexovccOperations()
		if err != nil {
			return err
		}
	}
	selected := make([]nexovcc.Card, 0, len(cards))
	for _, card := range cards {
		if scheduled && nexovccWithdrawnToReserve(card, ops) {
			continue
		}
		if cardID != "" && card.ID == cardID || cardID == "" && card.Status != "4" && card.NormalPending == (category == "normal_pending") {
			selected = append(selected, card)
		}
	}
	if cardID != "" && len(selected) == 0 {
		return fmt.Errorf("卡片不存在，未同步其他卡片")
	}
	skipped := len(cards) - len(selected)
	cards = selected
	cases, err := s.store.NexovccCases()
	if err != nil {
		return err
	}
	waiting := nexovccWaitingReturnCards(cases)
	// Requested returns come first. Within each priority use the oldest card
	// snapshot, so a scan interrupted by Pro work resumes with unfinished cards.
	sort.SliceStable(cards, func(i, j int) bool {
		if waiting[cards[i].ID] != waiting[cards[j].ID] {
			return waiting[cards[i].ID]
		}
		return f.CardSyncedAt(cards[i].Serial).Before(f.CardSyncedAt(cards[j].Serial))
	})
	var failures []error
	if account && (forceAccount || time.Since(f.AccountAt) >= time.Duration(cfg.WithSyncDefaults().AccountSyncSeconds)*time.Second) {
		cardAt := f.CardAt
		if e := s.syncNexovccAccountLocked(ctx); e != nil {
			return e
		}
		if err = s.store.NexovccCached("finance", &f); err != nil {
			return err
		}
		if f.CardAt == nil {
			f.CardAt = cardAt
		}
	}
	for i, card := range cards {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current, readErr := s.store.NexovccCard(card.ID)
		if readErr != nil {
			return readErr
		}
		if cardID == "" && (current.Status == "4" || current.NormalPending != (category == "normal_pending")) {
			continue
		}
		label := "同步正常卡余额与流水"
		if category == "normal_pending" {
			label = "同步正常待定卡余额与流水"
		}
		if cardID != "" {
			label = "同步本卡余额与流水"
		}
		s.nexovccProgress(fmt.Sprintf("%s %d/%d · 尾号 %s", label, i+1, len(cards), card.Last4), i, len(cards))
		e := s.syncNexovccCardSnapshotLocked(ctx, card.ID)
		if e == nil {
			e = s.afterNexovccCardSnapshotLocked(ctx, card.ID)
		}
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures = append(failures, fmt.Errorf("尾号 %s：%w", card.Last4, e))
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if err = s.store.CaptureNexovccRevenue(); err != nil {
		return err
	}
	message := fmt.Sprintf("已同步 %d 张正常卡余额与流水，%d 张正常待定或销卡保留已有数据", len(cards), skipped)
	if scheduled {
		message = fmt.Sprintf("已同步 %d 张正常卡余额与流水，%d 张已提现留 1U、正常待定或销卡保留已有数据", len(cards), skipped)
	}
	if category == "normal_pending" {
		message = fmt.Sprintf("已同步 %d 张正常待定卡余额、交易与资金明细", len(cards))
	}
	if cardID != "" {
		message = "本卡余额与交易明细已同步"
	}
	s.nexovccProgress(message, len(cards), len(cards))
	return nil
}

// Main-account records have their own cadence and never scan card ledgers.
func (s *Server) syncNexovccAccountLocked(ctx context.Context) error {
	c, _, err := s.nexovccClientLocked()
	if err != nil {
		return err
	}
	var f nexovcc.Finance
	if err = s.store.NexovccCached("finance", &f); err != nil {
		return err
	}
	since := f.AccountAt
	if since.IsZero() && f.CardAt == nil {
		since = f.At
	}
	// Account reads must not advance legacy per-card freshness.
	if f.CardAt == nil {
		cards, e := s.store.NexovccCards()
		if e != nil {
			return e
		}
		f.CardAt = map[string]time.Time{}
		for _, card := range cards {
			f.CardAt[card.Serial] = f.At
		}
	}
	if !since.IsZero() {
		since = since.Add(-72 * time.Hour).Truncate(time.Second)
	}
	s.nexovccProgress("更新近期充值记录与每日汇总", 0, 1)
	fresh, err := c.ReadAccountSummary(ctx, since)
	if err != nil {
		return err
	}
	return s.store.SaveNexovccFinance(nexovcc.MergeAccountSummary(f, fresh, since))
}

func (s *Server) nexovccFinanceNumberLocked(card nexovcc.Card) (string, error) {
	_, secret, err := s.store.GPTPayCard(card.ID)
	if err == nil && nexovcc.ValidFinanceCardNumber(secret.Number) {
		return secret.Number, nil
	}
	if number := s.nexovcc.financeNumbers[card.Serial]; nexovcc.ValidFinanceCardNumber(number) {
		return number, nil
	}
	return "", fmt.Errorf("缺少完整卡号，请同步卡片列表；未改查全账户流水")
}

func (s *Server) syncNexovccCardFinanceLocked(ctx context.Context, id string) error {
	return s.syncNexovccCardLedgerLocked(ctx, id, false)
}

func (s *Server) syncNexovccCardFundsLocked(ctx context.Context, id string) error {
	return s.syncNexovccCardLedgerLocked(ctx, id, true)
}

func (s *Server) syncNexovccCardLedgerLocked(ctx context.Context, id string, fundsOnly bool) error {
	if !fundsOnly {
		return s.syncNexovccCardSnapshotLocked(ctx, id)
	}
	card, err := s.store.NexovccCard(id)
	if err != nil {
		return err
	}
	number, err := s.nexovccFinanceNumberLocked(card)
	if err != nil {
		return err
	}
	client, _, err := s.nexovccClientLocked()
	if err != nil {
		return err
	}
	fresh, err := client.ReadCardFunds(ctx, number, card.Serial)
	if err != nil {
		return err
	}
	var previous nexovcc.Finance
	if err = s.store.NexovccCached("finance", &previous); err != nil {
		return err
	}
	return s.store.SaveNexovccFinance(nexovcc.MergeCardFunds(previous, fresh, card.Serial))
}

func (s *Server) nexovccCaseMatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GroupID string `json:"group_id"`
		Reason  string `json:"reason"`
		Confirm bool   `json:"confirm"`
	}
	if decodeJSON(w, r, &in, 4096) != nil {
		return
	}
	if !in.Confirm {
		writeAPI(w, 400, nil, "请确认账号与流水的对应关系")
		return
	}
	if err := s.store.MatchNexovccCase(r.PathValue("id"), in.GroupID, in.Reason); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]bool{"saved": true}, "")
}

func (s *Server) nexovccTradePurpose(w http.ResponseWriter, r *http.Request) {
	var in struct {
		NonPro   *bool `json:"non_pro"`
		Revision *int  `json:"revision"`
	}
	if decodeJSON(w, r, &in, 4096) != nil {
		return
	}
	if in.NonPro == nil || in.Revision == nil || *in.Revision < 0 {
		writeAPI(w, 400, nil, "请提供付款标记及当前版本")
		return
	}
	group, err := s.store.SetNexovccTradePurpose(r.PathValue("id"), *in.NonPro, *in.Revision)
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	writeAPI(w, 200, group, "")
}

// A rise in the aggregate wallet balance can be caused by another card or a
// recharge. Only the same card's uniquely identified transfer confirms arrival.
func (s *Server) nexovccTransferConfirmedLocked(ctx context.Context, op *nexovcc.Operation, kind int, amount string) bool {
	expected, err := nexovcc.Cents(amount)
	if err != nil {
		return false
	}
	if kind == 3002 && expected == 0 {
		op.Evidence = "卡台已销卡，销卡前余额为 0"
		return true
	}
	var f nexovcc.Finance
	if s.store.NexovccCached("finance", &f) != nil {
		return false
	}
	freshAfter := op.CreatedAt
	if op.StepAt.After(freshAfter) {
		freshAfter = op.StepAt
	}
	// Deleted cards are retired from routine finance scans. An unfinished close
	// still polls its own receipt until the principal has actually returned.
	if f.FundsSyncedAt(op.Serial).Before(freshAfter) || (kind == 3002 && !s.nexovccTransferEvidenceLocked(op, kind, expected, f)) {
		if s.syncNexovccCardFundsLocked(ctx, op.CardID) != nil {
			return false
		}
		if s.store.NexovccCached("finance", &f) != nil {
			return false
		}
	}
	if !s.nexovccTransferEvidenceLocked(op, kind, expected, f) {
		return false
	}
	_ = s.nexovccBalanceLocked(ctx)
	return true
}

// Match persisted ledger evidence without requiring another network request.
func (s *Server) nexovccTransferEvidenceLocked(op *nexovcc.Operation, kind int, expected int64, f nexovcc.Finance) bool {
	candidates := []nexovcc.Fund{}
	ops, err := s.store.NexovccOperations()
	if err != nil {
		return false
	}
	for _, r := range f.Funds {
		if r.Kind != kind || r.Serial != op.Serial || r.Currency != "USD" || r.At.Before(op.CreatedAt.Add(-time.Second)) || r.At.After(op.CreatedAt.Add(24*time.Hour)) {
			continue
		}
		value, err := nexovcc.SignedCents(r.Amount)
		if kind == 4001 {
			value = -value
		}
		if err != nil || value != expected {
			continue
		}
		used := false
		for _, prior := range ops {
			if prior.ID != op.ID && (prior.Evidence == "fund:"+r.ID || prior.WithdrawEvidence == "fund:"+r.ID) {
				used = true
				break
			}
		}
		if !used {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) != 1 {
		return false
	}
	op.Evidence = "fund:" + candidates[0].ID
	return true
}

func (s *Server) confirmCachedNexovccCloseLocked(op nexovcc.Operation) (nexovcc.Operation, bool) {
	if op.Terminal() || !nexovccClosing(op.Action) || (op.Step != "" && op.Step != "cancel_waiting" && op.Step != "cancel_submitting") {
		return op, false
	}
	card, err := s.store.NexovccCard(op.CardID)
	if err != nil || card.Status != "4" || card.Serial != op.Serial {
		return op, false
	}
	expected, err := nexovcc.Cents(op.BeforeBalance)
	if err != nil {
		return op, false
	}
	if expected == 0 {
		op.Evidence = "卡台已销卡，销卡前余额为 0"
	} else {
		var f nexovcc.Finance
		if s.store.NexovccCached("finance", &f) != nil || f.FundsSyncedAt(op.Serial).Before(op.CreatedAt) || f.FundsSyncedAt(op.Serial).Before(op.StepAt) || !s.nexovccTransferEvidenceLocked(&op, 3002, expected, f) {
			return op, false
		}
	}
	op.Status, op.Error = "succeeded", ""
	return op, true
}

// Refresh due categories even while the page is closed. It shares the
// operation mutex and can finish previously requested close pipelines after
// their withdrawal is confirmed. It never replays an uncertain submitted step.
func (s *Server) startNexovccMonitor() {
	ctx, cancel := context.WithCancel(context.Background())
	s.nexovcc.jobMu.Lock()
	s.nexovcc.monitorCancel = cancel
	s.nexovcc.wg.Add(1)
	s.nexovcc.jobMu.Unlock()
	s.startNexovccWithdrawMonitor(ctx)
	s.startNexovccImportMonitor(ctx)
	go func() {
		defer s.nexovcc.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			s.tickNexovccMonitor(ctx)
		}
	}()
}

// The five sync clocks run whenever the issuer is enabled, including idle accounts.
func (s *Server) tickNexovccMonitor(ctx context.Context) {
	bound, finishOperation, gateErr := s.nexovccOperationContext(ctx)
	if gateErr != nil {
		return
	}
	defer finishOperation()
	ctx = bound

	if ctx.Err() != nil {
		return
	}
	cfg, _, err := s.store.NexovccSettings()
	if err != nil || !cfg.Enabled {
		return
	}
	if cfg.AutoWithdrawEnabled {
		_, _ = s.runNexovccWithdrawCheck(ctx)
	}
	if !s.nexovcc.mu.TryLock() {
		return
	}
	if message, err := s.nexovccSyncWait(cfg, true); err != nil || message != "" {
		s.nexovcc.mu.Unlock()
		return
	}
	s.nexovcc.jobMu.Lock()
	if s.nexovcc.closed {
		s.nexovcc.jobMu.Unlock()
		s.nexovcc.mu.Unlock()
		return
	}
	jobID := randomRegistrationID()
	s.nexovcc.job = nexovccJob{ID: jobID, Status: "running", Message: "按分项间隔自动同步环球卡台", Total: 5}
	s.nexovcc.jobMu.Unlock()
	workCtx, stop := context.WithTimeout(ctx, 10*time.Minute)
	err = s.syncNexovccLocked(nexovccScheduledSync(workCtx))
	// Financial reads do not own Pro admission. Acquire both locks in the
	// established order only for automatic card mutations.
	s.nexovcc.mu.Unlock()
	if err == nil && s.tryBackgroundCardLock(&s.proCardAdmissionMu) {
		if s.nexovcc.mu.TryLock() {
			s.nexovcc.jobMu.Lock()
			sameJob := s.nexovcc.job.ID == jobID
			s.nexovcc.jobMu.Unlock()
			if sameJob {
				err = s.autoRecycleNexovccLocked(workCtx)
			}
			s.nexovcc.mu.Unlock()
		}
		s.proCardAdmissionMu.Unlock()
	}
	s.nexovcc.mu.Lock()
	stop()
	s.nexovcc.jobMu.Lock()
	if s.nexovcc.job.ID != jobID {
		s.nexovcc.jobMu.Unlock()
		s.nexovcc.mu.Unlock()
		return
	}
	s.nexovcc.job.Status = "completed"
	s.nexovcc.job.Message = "到期项目已同步"
	s.nexovcc.job.Done = s.nexovcc.job.Total
	if err != nil {
		s.nexovcc.job.Status = "failed"
		s.nexovcc.job.Message = fmt.Sprint(err)
		var waiting *nexovccCapacityWaitError
		if errors.As(err, &waiting) || errors.Is(err, errNexovccReadYielded) {
			s.nexovcc.job.Status = "completed"
		}
	}
	s.nexovcc.jobMu.Unlock()
	s.nexovcc.mu.Unlock()
}
