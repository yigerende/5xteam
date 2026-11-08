package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/ejh"
)

func autoBalanceCard(t *testing.T, s *Server, n int, status string, pending bool) ejh.CardRecord {
	t.Helper()
	remote := ejh.Card{OrderNo: fmt.Sprintf("balance-%d", n), Number: fmt.Sprintf("424242424242%04d", n), Validity: "1299", CVV: "123", Status: status, CardCurrency: "USD"}
	local, err := s.store.ImportEJHCard(remote, false)
	if err != nil {
		t.Fatal(err)
	}
	var card ejh.CardRecord
	if err = s.store.EJHGet("card", local.ID, &card); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	card.Balance, card.BalanceAt, card.BalanceAttemptAt, card.NormalPending = "7.00", &at, &at, pending
	if err = s.store.EJHPut("card", card.ID, card); err != nil {
		t.Fatal(err)
	}
	return card
}

func savedAutoBalanceCard(t *testing.T, s *Server, id string) ejh.CardRecord {
	t.Helper()
	var c ejh.CardRecord
	if err := s.store.EJHGet("card", id, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEJHAutoBalancesRefreshNormalOnly(t *testing.T) {
	s, _ := ejhTestServer(t, 200)
	normal := autoBalanceCard(t, s, 1, "3", false)
	frozen := autoBalanceCard(t, s, 2, "5", false)
	skipped := []ejh.CardRecord{autoBalanceCard(t, s, 3, "3", true), autoBalanceCard(t, s, 4, "8", false), autoBalanceCard(t, s, 5, "2", false)}
	unknown := autoBalanceCard(t, s, 6, "3", false)
	if err := s.store.EJHBeginOperation(ejh.Operation{ID: "cancel-unknown", CardID: unknown.ID, OrderNo: unknown.Remote.OrderNo, Method: "cancel", Status: "unknown", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	skipped = append(skipped, unknown)
	calls := map[string]int{}
	s.ejh.client.HTTP = ejhFixtureTransport(t, func(method string, data map[string]any) any {
		// No global card lock may be held while waiting for supplier I/O.
		if !s.ejh.mu.TryLock() {
			t.Fatal("automatic balance read holds card-operation lock")
		}
		s.ejh.mu.Unlock()
		if method == "account.balance.info" {
			calls["account"]++
			return map[string]any{"abroadList": []map[string]string{{"currency": "USD", "balance": "18.50"}}}
		}
		if method != "card.balance.info" {
			t.Fatalf("unexpected method %s", method)
		}
		order := fmt.Sprint(data["orderNo"])
		calls[order]++
		if order != normal.Remote.OrderNo && order != frozen.Remote.OrderNo {
			t.Fatalf("queried excluded card %s", order)
		}
		return map[string]string{"balance": "0.00"}
	})
	if err := s.syncEJHAutoBalances(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls["account"] != 1 {
		t.Fatal(calls)
	}
	for _, c := range []ejh.CardRecord{normal, frozen} {
		got := savedAutoBalanceCard(t, s, c.ID)
		if got.Balance != "0.00" || !got.BalanceAt.After(*c.BalanceAt) || got.BalanceError != "" {
			t.Fatal(got)
		}
	}
	for _, c := range skipped {
		got := savedAutoBalanceCard(t, s, c.ID)
		if got.Balance != c.Balance || !got.BalanceAt.Equal(*c.BalanceAt) || !got.BalanceAttemptAt.Equal(*c.BalanceAttemptAt) {
			t.Fatal("excluded snapshot changed", c.ID)
		}
	}
	var account struct {
		Data ejhAccountBalance `json:"data"`
		At   time.Time         `json:"at"`
	}
	if err := s.store.EJHGet("balance", "latest", &account); err != nil || account.Data.Balances[0].Balance != "18.50" || account.At.IsZero() {
		t.Fatal(account, err)
	}
	var state ejh.SyncState
	if err := s.store.EJHGet("balance_sync", "latest", &state); err != nil || state.LastSuccess == nil || state.Error != "" {
		t.Fatal(state, err)
	}
}

func TestEJHAutoBalancesFailureIsolation(t *testing.T) {
	s, _ := ejhTestServer(t, 200)
	bad := autoBalanceCard(t, s, 1, "3", false)
	good := autoBalanceCard(t, s, 2, "3", false)
	old := time.Now().Add(-time.Hour)
	if err := s.store.EJHPut("balance", "latest", map[string]any{"data": map[string]any{"abroadList": []map[string]string{{"currency": "USD", "balance": "9.00"}}}, "at": old}); err != nil {
		t.Fatal(err)
	}
	s.ejh.client.HTTP = ejhFixtureTransport(t, func(method string, data map[string]any) any {
		if method == "account.balance.info" || data["orderNo"] == bad.Remote.OrderNo {
			return map[string]any{}
		}
		return map[string]string{"balance": "-0.25"}
	})
	err := s.syncEJHAutoBalances(context.Background())
	if err == nil || !strings.Contains(err.Error(), "账户余额") || !strings.Contains(err.Error(), bad.ID) {
		t.Fatal(err)
	}
	failed := savedAutoBalanceCard(t, s, bad.ID)
	succeeded := savedAutoBalanceCard(t, s, good.ID)
	if failed.Balance != "7.00" || !failed.BalanceAt.Equal(*bad.BalanceAt) || failed.BalanceError == "" {
		t.Fatal("lost good snapshot", failed)
	}
	if succeeded.Balance != "-0.25" || succeeded.BalanceError != "" {
		t.Fatal("unrelated card not refreshed", succeeded)
	}
	var state ejh.SyncState
	if err = s.store.EJHGet("balance_sync", "latest", &state); err != nil || state.LastSuccess != nil || state.Error == "" {
		t.Fatal(state, err)
	}
	var account struct {
		Data ejhAccountBalance `json:"data"`
		At   time.Time         `json:"at"`
	}
	if err = s.store.EJHGet("balance", "latest", &account); err != nil || account.Data.Balances[0].Balance != "9.00" || !account.At.Equal(old) {
		t.Fatal("account balance overwritten", err)
	}
	s.ejh.client.HTTP = ejhFixtureTransport(t, func(method string, data map[string]any) any {
		if method == "account.balance.info" {
			return map[string]any{"abroadList": []map[string]string{{"currency": "USD", "balance": "10.00"}}}
		}
		return map[string]string{"balance": "2.00"}
	})
	if err = s.syncEJHAutoBalances(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := savedAutoBalanceCard(t, s, bad.ID); got.Balance != "2.00" || got.BalanceError != "" {
		t.Fatal("retry did not clear error", got)
	}
	if err = s.store.EJHGet("balance_sync", "latest", &state); err != nil || state.LastSuccess == nil || state.Error != "" {
		t.Fatal(state, err)
	}
}

func TestEJHAutoBalancesConcurrentChanges(t *testing.T) {
	for _, change := range []string{"pending", "closing", "cancel_unknown", "newer_balance", "metadata"} {
		t.Run(change, func(t *testing.T) {
			s, _ := ejhTestServer(t, 200)
			card := autoBalanceCard(t, s, 1, "3", false)
			s.ejh.client.HTTP = ejhFixtureTransport(t, func(method string, data map[string]any) any {
				if method == "account.balance.info" {
					return map[string]any{"abroadList": []map[string]string{{"currency": "USD", "balance": "10.00"}}}
				}
				current := savedAutoBalanceCard(t, s, card.ID)
				switch change {
				case "pending":
					current.NormalPending = true
				case "closing":
					current.Remote.Status = "8"
				case "cancel_unknown":
					if err := s.store.EJHBeginOperation(ejh.Operation{ID: "cancel-during-read", Method: "cancel", Status: "unknown", CardID: card.ID, OrderNo: card.Remote.OrderNo, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
						t.Fatal(err)
					}
				case "newer_balance":
					now := time.Now()
					current.Balance = "99.00"
					current.BalanceAt = &now
					current.BalanceAttemptAt = &now
				case "metadata":
					current.Remote.Note = "updated while reading"
				}
				if err := s.store.EJHPut("card", current.ID, current); err != nil {
					t.Fatal(err)
				}
				return map[string]string{"balance": "1.00"}
			})
			if err := s.syncEJHAutoBalances(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := savedAutoBalanceCard(t, s, card.ID)
			switch change {
			case "newer_balance":
				if got.Balance != "99.00" {
					t.Fatal("newer manual balance overwritten")
				}
			case "metadata":
				if got.Balance != "1.00" || got.Remote.Note != "updated while reading" {
					t.Fatal("metadata overwritten", got)
				}
			default:
				if got.Balance != card.Balance || !got.BalanceAt.Equal(*card.BalanceAt) {
					t.Fatal("changed category was overwritten", got)
				}
			}
		})
	}
}

func TestEJHAutoBalancesDisabledAndCancelled(t *testing.T) {
	s, _ := ejhTestServer(t, 200)
	autoBalanceCard(t, s, 1, "3", false)
	s.ejh.client.HTTP = ejhFixtureTransport(t, func(method string, data map[string]any) any {
		t.Fatal("disabled scheduler contacted issuer")
		return nil
	})
	cfg, _, err := s.store.EJHSettings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	if _, err = s.store.SaveEJHSettings(cfg, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.syncEJHAutoBalances(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	if _, err = s.store.SaveEJHSettings(cfg, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.syncEJHAutoBalances(ctx); err != context.Canceled {
		t.Fatalf("cancelled scheduler: %v", err)
	}
}
