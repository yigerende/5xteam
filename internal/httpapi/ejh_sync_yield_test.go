package httpapi

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/ejh"
)

func TestEJHSyncYieldsToAutomaticPreparationAndReloadsCard(t *testing.T) {
	s, _ := ejhTestServer(t, 200)
	t.Cleanup(s.stopEJH)
	remote := ejh.Card{OrderNo: "yield-card", Number: "4242424242424242", CVV: "123", Validity: "1299", Status: "1", CardCurrency: "USD"}
	card, err := s.store.ImportEJHCard(remote, true)
	if err != nil {
		t.Fatal(err)
	}
	started, finishRequest := make(chan struct{}), make(chan struct{})
	var trades atomic.Int32
	s.ejh.client.HTTP = ejhFixtureTransport(t, func(method string, _ map[string]any) any {
		if method == "card.list.info" {
			close(started)
			<-finishRequest
			return map[string]any{"cardInfos": []ejh.Card{remote}}
		}
		trades.Add(1)
		return map[string]any{"tradeInfos": []ejh.Trade{}}
	})
	task, err := s.requestEJHSync("yield", time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("sync not started")
	}
	// This is the same priority held by automatic reserveProStartCard. It must
	// get the lock before sync starts another card's remote transaction query.
	s.proCardPreparing.Add(1)
	close(finishRequest)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = lockProCardPreparation(ctx, &s.ejh.mu, "test"); err != nil {
		s.proCardPreparing.Add(-1)
		t.Fatal("automatic preparation starved", err)
	}
	var current ejh.CardRecord
	err = s.store.EJHGet("card", card.ID, &current)
	if err == nil {
		current.NormalPending = true
		err = s.store.EJHPut("card", card.ID, current)
	}
	s.ejh.mu.Unlock()
	s.proCardPreparing.Add(-1)
	if err != nil {
		t.Fatal(err)
	}
	result := awaitEJHSyncFixture(t, s, task.result.ID)
	if result.Status != "completed" || trades.Load() != 0 {
		t.Fatal("sync used stale card category after yielding", result, trades.Load())
	}
}
