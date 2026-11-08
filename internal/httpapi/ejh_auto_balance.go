package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/store"
)

type ejhAccountBalance struct {
	AppKey   string `json:"appKey"`
	Balances []struct {
		Currency string `json:"currency"`
		Name     string `json:"currencyName"`
		Balance  string `json:"balance"`
	} `json:"abroadList"`
}

// Balance reads run after trade synchronization, without extending its global
// card-operation lock. Only short snapshot commits take that lock. A single
// scheduler owns this pass; no browser or second timer starts another one.
func (s *Server) syncEJHAutoBalances(parent context.Context) (retErr error) {
	if err := parent.Err(); err != nil {
		return err
	}
	cfg, key, err := s.ejhConfig()
	if err != nil || !cfg.Enabled {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	var state ejh.SyncState
	_ = s.store.EJHGet("balance_sync", "latest", &state)
	started := time.Now()
	state.LastAttempt = &started
	defer func() {
		state.Error = ""
		if retErr != nil {
			state.Error = retErr.Error()
		} else {
			finished := time.Now()
			state.LastSuccess = &finished
		}
		retErr = errors.Join(retErr, s.store.EJHPut("balance_sync", "latest", state))
	}()
	var failures []error
	if err = s.refreshEJHAutoAccountBalance(ctx, cfg, key); err != nil {
		failures = append(failures, fmt.Errorf("账户余额更新失败：%w", err))
	}
	cards, err := store.EJHRecords[ejh.CardRecord](s.store, "card")
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	// A slow/failing card must not starve the same later cards every cycle.
	sort.Slice(cards, func(i, j int) bool {
		a, b := cards[i].BalanceAttemptAt, cards[j].BalanceAttemptAt
		if a == nil || b == nil {
			if a == nil && b == nil {
				return cards[i].ID < cards[j].ID
			}
			return a == nil
		}
		if a.Equal(*b) {
			return cards[i].ID < cards[j].ID
		}
		return a.Before(*b)
	})
	for _, card := range cards {
		if err = ctx.Err(); err != nil {
			failures = append(failures, fmt.Errorf("余额更新未全部完成，下轮继续：%w", err))
			break
		}
		currentCfg, currentKey, configErr := s.ejhConfig()
		if configErr != nil {
			return errors.Join(append(failures, configErr)...)
		}
		if !currentCfg.Enabled {
			break
		}
		cfg, key = currentCfg, currentKey
		if !s.ejhAutoBalanceEligible(card) {
			continue
		}
		if err = s.refreshEJHAutoCardBalance(ctx, cfg, key, card); err != nil {
			failures = append(failures, fmt.Errorf("卡片 %s 余额更新失败：%w", card.ID, err))
		}
	}
	return errors.Join(failures...)
}

// Match the page's normal lifecycle, including frozen/expired cards in that
// category. Pending classification and all cancellation states stay excluded.
func (s *Server) ejhAutoBalanceEligible(card ejh.CardRecord) bool {
	if card.NormalPending || card.Remote.OrderNo == "" || card.Remote.Status == "2" || card.Remote.Status == "8" {
		return false
	}
	local, _, err := s.store.GPTPayCard(card.ID)
	return err == nil && !local.NormalPending && local.IssuerStatus != "2" && local.IssuerStatus != "8" && local.IssuerCancelStatus == ""
}

func (s *Server) ejhBalanceCommit(ctx context.Context, fn func() error) error {
	wait, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	unlock, err := s.lockBackgroundCard(wait, &s.ejh.mu)
	if err != nil {
		return fmt.Errorf("卡台正在处理其他操作，保留原余额，下轮重试")
	}
	defer unlock()
	return fn()
}

func (s *Server) refreshEJHAutoAccountBalance(ctx context.Context, cfg ejh.Settings, key string) error {
	started := time.Now()
	request, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var out ejhAccountBalance
	err := s.ejhClient().Call(request, cfg, key, "account.balance.info", map[string]any{}, &out)
	if err != nil {
		return err
	}
	if out.Balances == nil {
		return fmt.Errorf("卡台未返回账户余额，保留上次查询结果")
	}
	seen := map[string]bool{}
	for _, b := range out.Balances {
		_, err = ejh.Money(strings.TrimPrefix(b.Balance, "-"))
		if err != nil || !ejh.ValidCurrency(b.Currency) || seen[b.Currency] {
			return fmt.Errorf("卡台余额或币种无效，保留上次查询结果")
		}
		seen[b.Currency] = true
	}
	return s.ejhBalanceCommit(ctx, func() error {
		var saved struct {
			At time.Time `json:"at"`
		}
		err := s.store.EJHGet("balance", "latest", &saved)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && !saved.At.Before(started) {
			return nil
		}
		current, _, err := s.ejhConfig()
		if err != nil {
			return err
		}
		if current.URL != cfg.URL || current.AppKey != cfg.AppKey {
			return fmt.Errorf("卡台配置已改变，保留原余额")
		}
		return s.store.EJHPut("balance", "latest", map[string]any{"data": out, "at": time.Now()})
	})
}

func (s *Server) refreshEJHAutoCardBalance(ctx context.Context, cfg ejh.Settings, key string, expected ejh.CardRecord) error {
	started := time.Now()
	request, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var out struct {
		Balance string `json:"balance"`
	}
	queryErr := s.ejhClient().Call(request, cfg, key, "card.balance.info", map[string]any{"orderNo": expected.Remote.OrderNo}, &out)
	if queryErr == nil {
		_, queryErr = ejh.Money(strings.TrimPrefix(out.Balance, "-"))
	}
	saveErr := s.ejhBalanceCommit(ctx, func() error {
		var card ejh.CardRecord
		if err := s.store.EJHGet("card", expected.ID, &card); err != nil {
			return err
		}
		if card.Remote.OrderNo != expected.Remote.OrderNo || !s.ejhAutoBalanceEligible(card) {
			return nil
		}
		// Do not overwrite a newer manual result, or any intervening metadata.
		if card.BalanceAttemptAt != nil && !card.BalanceAttemptAt.Before(started) {
			return nil
		}
		now := time.Now()
		card.BalanceAttemptAt = &now
		if queryErr != nil {
			card.BalanceError = queryErr.Error()
		} else {
			card.Balance, card.BalanceAt, card.BalanceError = out.Balance, &now, ""
		}
		return s.store.EJHPut("card", card.ID, card)
	})
	return errors.Join(queryErr, saveErr)
}
