package httpapi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManualCardPriorityHoldsAcrossRetryAndExpires(t *testing.T) {
	s := &Server{}
	first, second := s.beginManualCardPreparation(), s.beginManualCardPreparation()
	var lock sync.Mutex
	if s.tryBackgroundCardLock(&lock) {
		t.Fatal("background passed manual priority")
	}
	first(false)
	first(false) // deferred cleanup must be idempotent
	if !s.manualCardPreparationPending() {
		t.Fatal("one finish dropped another manual request")
	}
	second(true)
	if !s.manualCardPreparationPending() || s.tryBackgroundCardLock(&lock) {
		t.Fatal("background stole retry gap")
	}
	s.proCardPriorityUntil.Store(time.Now().Add(-time.Second).UnixNano())
	if s.manualCardPreparationPending() || !s.tryBackgroundCardLock(&lock) {
		t.Fatal("abandoned wait blocked maintenance")
	}
	lock.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.lockBackgroundCard(ctx, &lock); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestManualCardPriorityQueuesEJHSyncThenResumes(t *testing.T) {
	s, _ := ejhTestServer(t, 200)
	t.Cleanup(s.stopEJH)
	var calls atomic.Int32
	s.ejh.client.HTTP = ejhFixtureTransport(t, func(method string, _ map[string]any) any {
		if method != "card.list.info" {
			t.Errorf("unexpected call %s", method)
		}
		calls.Add(1)
		return map[string]any{"cardInfos": []any{}}
	})
	finish := s.beginManualCardPreparation()
	defer finish(false)
	result := requestEJHSyncFixture(t, s, `{"async":true}`, context.Background())
	s.tickEJHCancellations(context.Background())
	s.tickEJHAutoCancel(context.Background())
	s.ejh.syncMu.Lock()
	task := s.ejh.syncJobs[result.ID]
	s.ejh.syncMu.Unlock()
	select {
	case <-task.done:
		t.Fatal("sync ran while manual admission has priority")
	case <-time.After(100 * time.Millisecond):
	}
	if calls.Load() != 0 || s.ejhSyncSnapshot(task).Status != "queued" {
		t.Fatal("maintenance did not yield")
	}
	finish(false)
	if out := awaitEJHSyncFixture(t, s, result.ID); out.Status != "completed" || calls.Load() != 1 {
		t.Fatal("queued sync did not resume", out)
	}
}

func TestManualCardPriorityDefersScheduledIssuance(t *testing.T) {
	s, issued, paid, email := allocationFixture(t)
	finish := s.beginManualCardPreparation()
	defer finish(false)
	ctx := context.WithValue(context.Background(), proScheduledContextKey{}, true)
	_, _, err := s.reserveProStartCard(ctx, email, "", true, false)
	var waiting *proCardPreparationWait
	if !errors.As(err, &waiting) || issued.Load() != 0 || paid.Load() != 0 {
		t.Fatal("scheduled work overtook manual", err)
	}
	finish(false)
	if _, _, err = s.reserveProStartCard(ctx, email, "", true, false); err != nil || issued.Load() != 1 || paid.Load() != 0 {
		t.Fatal("scheduled work could not resume", err)
	}
}
