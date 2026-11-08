package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/model"
)

func TestProCardBusyRetryResumesOnceAndHonorsStop(t *testing.T) {
	for _, gate := range []string{"admission", "issuer", "stopped"} {
		t.Run(gate, func(t *testing.T) {
			s, issued, paid, email := allocationFixture(t)
			s.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
				return errors.New("test: no real login")
			}}
			lock := &s.ejh.mu
			if gate == "admission" {
				lock = &s.proCardAdmissionMu
			}
			lock.Lock()
			reply := payCall(t, s.startProAutomation, "email", email, map[string]any{"plan_code": "pro20"})
			lock.Unlock()
			if reply.Code != 409 || reply.Header().Get("X-Pro-Card-Wait") != "preparation" || reply.Header().Get("X-Pro-Card-Retry") != "busy" || issued.Load() != 0 || paid.Load() != 0 {
				t.Fatalf("busy admission was not rejected before side effects: %d %s", reply.Code, reply.Body.String())
			}
			var body struct {
				Data proCardPreparationRetry `json:"data"`
			}
			if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			input := map[string]any{"plan_code": "pro20", "card_preparation_retry": body.Data}
			if gate == "stopped" {
				if r := payCall(t, s.stopProAutomation, "email", email, nil); r.Code != 200 {
					t.Fatal(r.Body.String())
				}
				if r := payCall(t, s.startProAutomation, "email", email, input); r.Code != 409 || issued.Load() != 0 || paid.Load() != 0 {
					t.Fatal("delayed retry undid Stop", r.Body.String())
				}
				return
			}
			if r := payCall(t, s.startProAutomation, "email", email, input); r.Code != 202 {
				t.Fatal(r.Body.String())
			}
			waitProAutoFinished(t, s, email)
			if issued.Load() != 1 || paid.Load() != 0 {
				t.Fatal("retry did not issue exactly once", issued.Load(), paid.Load())
			}
			if r := payCall(t, s.startProAutomation, "email", email, input); r.Code != 409 || issued.Load() != 1 {
				t.Fatal("stale retry started another flow", r.Body.String())
			}
		})
	}
}

func TestProCardPreparationHoldIsNotBusyRetry(t *testing.T) {
	s, issued, paid, email := allocationFixture(t)
	op := ejh.Operation{ID: "unconfirmed-opening", Method: "open", Status: "unknown", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.store.EJHPut("operation", op.ID, op); err != nil {
		t.Fatal(err)
	}
	r := payCall(t, s.startProAutomation, "email", email, map[string]any{"plan_code": "pro20"})
	if r.Code != 409 || r.Header().Get("X-Pro-Card-Wait") != "preparation" || r.Header().Get("X-Pro-Card-Retry") != "" || issued.Load() != 0 || paid.Load() != 0 {
		t.Fatal("financial hold became an automatic retry", r.Body.String())
	}
}

func TestProCardPreparationRetryRejectsChangedProgress(t *testing.T) {
	s, issued, paid, email := allocationFixture(t)
	before, _, _ := s.store.MailAccountCredential(email)
	for _, changed := range []model.ProAutoState{
		{ID: "new-flow"}, {OrderID: "paid-order"}, {Status: "completed"}, {StopVersion: 1}, {StopRequested: true},
	} {
		if _, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto = changed }); err != nil {
			t.Fatal(err)
		}
		r := payCall(t, s.startProAutomation, "email", email, map[string]any{"plan_code": "pro20", "card_preparation_retry": cardPreparationProgress(before.ProAuto)})
		if r.Code != 409 || issued.Load() != 0 || paid.Load() != 0 || r.Header().Get("X-Pro-Card-Retry") != "" {
			t.Fatal("changed progress admitted", r.Body.String())
		}
	}
}

func TestProCardRetryRechecksProgressAfterWaiting(t *testing.T) {
	s, issued, paid, email := allocationFixture(t)
	p, _, _ := s.store.MailAccountCredential(email)
	guard := cardPreparationProgress(p.ProAuto)
	accountLock := &sync.Mutex{}
	s.proLocks.Store("account:"+email, accountLock)
	s.proCardAdmissionMu.Lock()
	released := false
	defer func() {
		if !released {
			s.proCardAdmissionMu.Unlock()
		}
	}()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- payCall(t, s.startProAutomation, "email", email, map[string]any{"plan_code": "pro20", "card_preparation_retry": guard})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for accountLock.TryLock() {
		accountLock.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("retry did not enter admission")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.Status = "completed" }); err != nil {
		t.Fatal(err)
	}
	s.proCardAdmissionMu.Unlock()
	released = true
	select {
	case r := <-done:
		if r.Code != 409 || issued.Load() != 0 || paid.Load() != 0 {
			t.Fatal("waiting retry ignored progress", r.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry did not finish")
	}
}
