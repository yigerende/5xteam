package httpapi

import (
	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/store"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

func (s *Server) syncEJH(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Start string `json:"start"`
		End   string `json:"end"`
		Async bool   `json:"async"`
	}
	if decodeJSON(w, r, &in, 4096) != nil {
		return
	}
	end := time.Now()
	start := end.AddDate(0, 0, -90)
	var e error
	zone := time.FixedZone("CST", 8*3600)
	if in.Start != "" {
		start, e = time.ParseInLocation("2006-01-02", in.Start, zone)
		if e != nil {
			writeAPI(w, 400, nil, "开始日期无效")
			return
		}
	}
	if in.End != "" {
		end, e = time.ParseInLocation("2006-01-02", in.End, zone)
		end = end.Add(24*time.Hour - time.Second)
		if e != nil {
			writeAPI(w, 400, nil, "结束日期无效")
			return
		}
	}
	if !start.Before(end) || end.Sub(start) > 366*24*time.Hour {
		writeAPI(w, 400, nil, "查询范围须为 1～366 天，后台按 90 天分段")
		return
	}
	key := start.Format(time.RFC3339Nano) + "/" + end.Format(time.RFC3339Nano)
	if in.Start == "" && in.End == "" {
		key = ejhRecentSyncKey(end)
	}
	task, e := s.requestEJHSync(key, start, end)
	if e != nil {
		writeAPI(w, 503, nil, e.Error())
		return
	}
	if in.Async {
		writeAPI(w, 202, s.ejhSyncSnapshot(task), "")
		return
	}
	// Older clients can still wait for completion. Disconnecting a browser does
	// not cancel the shared, bounded read-only task used by other clients.
	select {
	case <-r.Context().Done():
		return
	case <-task.done:
	}
	if result := s.ejhSyncSnapshot(task); result.Status != "completed" {
		writeAPI(w, 502, nil, result.Error)
		return
	}
	writeAPI(w, 200, map[string]bool{"synced": true}, "")
}
func (s *Server) syncEJHLocked(ctx context.Context, start, end time.Time) (retErr error) {
	var state ejh.SyncState
	_ = s.store.EJHGet("sync", "latest", &state)
	now := time.Now()
	state.LastAttempt = &now
	defer func() {
		if retErr != nil {
			state.Error = retErr.Error()
		} else {
			state.LastSuccess = &now
			state.Error = ""
		}
		if e := s.store.EJHPut("sync", "latest", state); e != nil && retErr == nil {
			retErr = e
		}
	}()
	cfg, key, e := s.ejhConfig()
	if e != nil {
		return e
	}
	savedCards, e := store.EJHRecords[ejh.CardRecord](s.store, "card")
	if e != nil {
		return e
	}
	closed := map[string]bool{}
	for _, c := range savedCards {
		if c.Remote.Status == "2" || c.NormalPending {
			closed[c.Remote.OrderNo] = true
		}
	}
	client := s.ejhClient()
	seenCards := map[[32]byte]bool{}
	cardErrors := 0
	for page := 1; page <= 10000; page++ {
		if e = ctx.Err(); e != nil {
			return e
		}
		var out struct {
			Cards []ejh.Card `json:"cardInfos"`
		}
		if e = client.Call(ctx, cfg, key, "card.list.info", map[string]any{"page": page, "size": 100}, &out); e != nil {
			return e
		}
		if out.Cards == nil {
			return fmt.Errorf("卡台响应缺少有效 cardInfos 列表，不能确认卡片同步完整")
		}
		for _, remote := range out.Cards {
			if closed[remote.OrderNo] {
				continue
			}
			if _, e = s.store.ImportEJHCard(remote, false); e != nil {
				cardErrors++
				var rec ejh.CardRecord
				if s.store.EJHGet("card", ejhCardID(remote.OrderNo), &rec) == nil {
					rec.Error = e.Error()
					if saveErr := s.store.EJHPut("card", rec.ID, rec); saveErr != nil {
						return saveErr
					}
				}
			}
		}
		raw, _ := json.Marshal(out.Cards)
		hash := sha256.Sum256(raw)
		if len(out.Cards) > 0 && seenCards[hash] {
			return fmt.Errorf("卡片接口重复返回同一页，已停止同步")
		}
		seenCards[hash] = true
		if len(out.Cards) < 100 {
			break
		}
		if page == 10000 {
			return fmt.Errorf("卡片分页未结束，请检查供应商返回")
		}
	}
	cards, e := store.EJHRecords[ejh.CardRecord](s.store, "card")
	if e != nil {
		return e
	}
	trades, e := store.EJHRecords[ejh.TradeRecord](s.store, "trade")
	if e != nil {
		return e
	}
	settledClosures := ejhSettledClosures(trades)
	var failures []error
	for _, card := range cards {
		if card.NormalPending || card.Remote.OrderNo == "" || card.Remote.Status == "2" && settledClosures[card.ID] {
			continue
		}
		if e = ctx.Err(); e != nil {
			failures = append(failures, e)
			break
		}
		if e = s.collectEJHCardTrades(ctx, client, cfg, key, card, start, end); e != nil {
			failures = append(failures, fmt.Errorf("卡片订单 %s 交易更新失败：%w", card.Remote.OrderNo, e))
		}
	}
	// A failed card retains its previous complete snapshot. Reconcile the cards
	// that did finish, even if another card failed or the request was cancelled.
	if e = s.store.EJHReconcile(); e != nil {
		failures = append(failures, fmt.Errorf("自动对账失败：%w", e))
	}
	if cardErrors > 0 {
		failures = append(failures, fmt.Errorf("%d 张卡片资料不完整，未加入 Pro，请在卡片页面刷新详情", cardErrors))
	}
	return errors.Join(failures...)
}

// Cancellation can finish before the issuer publishes its refund. Closed cards
// keep their saved details/balance, but need trade polling until every known
// closure refund is posted with both amounts. Derive this from persisted trades
// so pre-fix cards and interrupted runs also recover without a migration.
func ejhSettledClosures(records []ejh.TradeRecord) map[string]bool {
	settled := map[string]bool{}
	for _, record := range records {
		t := record.Trade
		if t.Type != "17" {
			continue
		}
		if complete, seen := settled[record.CardID]; seen && !complete {
			continue
		}
		_, transactionErr := ejh.Money(t.TradeAmount)
		_, settlementErr := ejh.Money(t.BillAmount)
		settled[record.CardID] = t.Status == "2" && transactionErr == nil && settlementErr == nil &&
			ejh.ValidCurrency(t.TradeCurrency) && ejh.ValidCurrency(t.BillCurrency)
	}
	return settled
}

func (s *Server) collectEJHCardTrades(ctx context.Context, client *ejh.Client, cfg ejh.Settings, key string, card ejh.CardRecord, start, end time.Time) error {
	collected := []ejh.Trade{}
	zone := time.FixedZone("CST", 8*3600)
	for from := start; from.Before(end); {
		to := from.Add(89 * 24 * time.Hour)
		if to.After(end) {
			to = end
		}
		seenTrades := map[[32]byte]bool{}
		for page := 1; page <= 10000; page++ {
			if e := ctx.Err(); e != nil {
				return e
			}
			var out struct {
				Trades []ejh.Trade `json:"tradeInfos"`
			}
			if e := client.Call(ctx, cfg, key, "card.trade.info", map[string]any{"orderNo": card.Remote.OrderNo, "tradeTime": from.In(zone).Format("2006-01-02 15:04:05"), "endTime": to.In(zone).Format("2006-01-02 15:04:05"), "page": page, "size": 100}, &out); e != nil {
				return e
			}
			if out.Trades == nil || len(out.Trades) > 100 {
				return fmt.Errorf("卡台响应缺少有效 tradeInfos 列表，不能确认流水同步完整")
			}
			ids := map[string]bool{}
			for _, t := range out.Trades {
				id := t.TradeNo + "|" + t.Type
				if t.OrderNo != card.Remote.OrderNo || t.TradeNo == "" || t.Type == "" || ids[id] {
					return fmt.Errorf("卡台交易归属或流水编号异常")
				}
				ids[id] = true
			}
			collected = append(collected, out.Trades...)
			raw, _ := json.Marshal(out.Trades)
			hash := sha256.Sum256(raw)
			if len(out.Trades) > 0 && seenTrades[hash] {
				return fmt.Errorf("交易接口重复返回同一页，已停止同步")
			}
			seenTrades[hash] = true
			if len(out.Trades) < 100 {
				break
			}
			if page == 10000 {
				return fmt.Errorf("交易分页未结束，请缩小时间范围")
			}
		}
		from = to
	}
	return s.store.EJHSaveTrades(card.ID, collected)
}
func (s *Server) startEJH(parent context.Context) {
	s.ejh.lifecycle.Lock()
	if s.ejh.cancel != nil {
		s.ejh.lifecycle.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.ejh.cancel = cancel
	s.ejh.ctx = ctx
	s.ejh.wg.Add(4)
	s.ejh.lifecycle.Unlock()
	go func() {
		defer s.ejh.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.tickEJHAutoCancel(ctx)
			}
		}
	}()
	go func() {
		defer s.ejh.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			s.captureEJHFinance()
			s.syncEJHDailyConsumption(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer s.ejh.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.tickProForceMerge(ctx)
			}
		}
	}()
	go func() {
		defer s.ejh.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		next := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if time.Now().Before(next) {
					s.tickEJHCancellations(ctx)
					continue
				}
				cfg, _, e := s.store.EJHSettings()
				if e != nil || !cfg.Enabled {
					next = time.Now().Add(time.Minute)
					continue
				}
				now := time.Now()
				task, err := s.requestEJHSync(ejhRecentSyncKey(now), now.AddDate(0, 0, -90), now)
				if err != nil {
					next = now.Add(5 * time.Second)
					continue
				}
				select {
				case <-ctx.Done():
					return
				case <-task.done:
				}
				// Separate read pass: a trade error must not suppress balance
				// updates, and slow balance I/O must not hold the sync lock.
				_ = s.syncEJHAutoBalances(ctx)
				next = time.Now().Add(time.Duration(cfg.SyncIntervalSeconds) * time.Second)
			}
		}
	}()
}
func (s *Server) stopEJH() {
	s.ejh.lifecycle.Lock()
	s.ejh.syncClosed = true
	if s.ejh.cancel != nil {
		s.ejh.cancel()
	}
	s.ejh.syncMu.Lock()
	for _, task := range s.ejh.syncJobs {
		task.cancel()
	}
	s.ejh.syncMu.Unlock()
	s.ejh.lifecycle.Unlock()
	s.ejh.wg.Wait()
}
