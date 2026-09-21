package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func seedGPTPayPollOrder(t *testing.T, s *Server, id, email, remoteID string) gptpay.Order {
	t.Helper()
	if _, err := s.store.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email}); err != nil {
		t.Fatal(err)
	}
	s.store.UpdateMailAccountManagementScope(email, "pro")
	cfg, key, err := s.store.GPTPaySettings()
	if err != nil {
		t.Fatal(err)
	}
	card, secret, err := s.store.GPTPayCard("test-card")
	if err != nil {
		t.Fatal(err)
	}
	o := gptpay.Order{ID: id, Email: email, CardID: card.ID, CardLast4: card.Last4, Status: "processing", CreatedAt: time.Now().Add(-time.Minute), Remote: gptpay.RemoteOrder{ID: remoteID, Status: "processing"}}
	if err := s.store.CreateGPTPayOrder(o, gptpay.Snapshot{URL: cfg.URL, APIKey: key, Input: gptpay.CreateInput{CardSecret: secret}}); err != nil {
		t.Fatal(err)
	}
	s.updateProPaymentStage(o)
	return o
}

func TestGPTPayBackgroundSyncSurvivesClosedDialogAndRestart(t *testing.T) {
	var creates, reads atomic.Int32
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "test-api-key" {
			t.Error("did not use original order credentials")
		}
		if strings.HasSuffix(r.URL.Path, "/status") {
			reads.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"orders":[{"id":"remote-background","status":"success","settlementStatus":"settled","cancellationStatus":"failed","chargedCredits":80}]}}`)
		} else {
			creates.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"id":"remote-background","status":"processing","cancellationStatus":"waiting"}}`)
		}
	})
	o := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, payInput("background-restart-001")))
	_, original, _ := st.MailAccountCredential(email)
	s.Close()
	// No dialog refresh calls. Recover the stored order in a new server instance.
	s2, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err = st.SaveGPTPaySettings(gptpay.Settings{}, "different-current-key"); err != nil {
		t.Fatal(err)
	}
	s2.startGPTPayOrderMonitor(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	var p model.MailAccountProfile
	for time.Now().Before(deadline) {
		p, _, _ = st.MailAccountCredential(email)
		if p.ProManualStages["recharge"].Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s2.stopGPTPayOrderMonitor()
	saved, _, err := st.GPTPayOrder(o.ID)
	if err != nil || saved.Status != "success" || saved.Remote.CancellationStatus != "failed" || saved.LastStatusCheckAt == nil || saved.NextStatusCheckAt != nil || p.ProManualStages["recharge"].Status != "completed" {
		t.Fatalf("did not finish: %+v %+v %v", saved, p.ProManualStages, err)
	}
	_, current, _ := st.MailAccountCredential(email)
	if current.AccessToken != original.AccessToken || current.RefreshToken != original.RefreshToken {
		t.Fatal("sync changed credentials")
	}
	s2.pollGPTPayOrders(context.Background(), time.Now().Add(time.Hour))
	if creates.Load() != 1 || reads.Load() != 1 {
		t.Fatalf("duplicate create/read: %d %d", creates.Load(), reads.Load())
	}
	card, _, err := st.GPTPayCard("test-card")
	if err != nil || card.OpenedAccounts != 1 || card.PendingAccounts != 0 {
		t.Fatalf("card usage not reconciled: %+v %v", card, err)
	}
}

func TestGPTPayBackgroundFailureBackoffAndRecovery(t *testing.T) {
	for _, failure := range []string{"http_error", "missing", "unknown", "not_found", "deadline"} {
		t.Run(failure, func(t *testing.T) {
			var reads atomic.Int32
			var recovered atomic.Bool
			s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/status") {
					t.Error("background submitted purchase")
					return
				}
				reads.Add(1)
				if !recovered.Load() {
					switch failure {
					case "http_error":
						w.WriteHeader(503)
						fmt.Fprint(w, `{"code":50301}`)
					case "missing":
						fmt.Fprint(w, `{"code":0,"data":{"orders":[]}}`)
					case "unknown":
						fmt.Fprint(w, `{"code":0,"data":{"orders":[{"id":"remote-poll","status":"unrecognized"}]}}`)
					case "not_found":
						fmt.Fprint(w, `{"code":0,"data":{"orders":[{"id":"remote-poll","status":"not_found"}]}}`)
					case "deadline":
						io.Copy(io.Discard, r.Body)
						select {
						case <-r.Context().Done():
						case <-time.After(time.Second):
						}
					}
					return
				}
				fmt.Fprint(w, `{"code":0,"data":{"orders":[{"id":"remote-poll","status":"success","cancellationStatus":"success"}]}}`)
			})
			o := seedGPTPayPollOrder(t, s, "poll-fail", email, "remote-poll")
			if failure == "deadline" {
				s.gptpay = gptpay.New()
				s.gptpay.HTTP.Timeout = 20 * time.Millisecond
			}
			s.pollGPTPayOrders(context.Background(), time.Now())
			saved, _, err := st.GPTPayOrder(o.ID)
			if err != nil || saved.Status != "processing" || saved.StatusCheckError == "" || saved.Error != "" || saved.NextStatusCheckAt == nil || time.Until(*saved.NextStatusCheckAt) < 50*time.Second {
				t.Fatalf("error destroyed state/backoff: %+v %v", saved, err)
			}
			s.pollGPTPayOrders(context.Background(), time.Now())
			if reads.Load() != 1 {
				t.Fatal("no backoff")
			}
			recovered.Store(true)
			s.pollGPTPayOrders(context.Background(), time.Now().Add(2*time.Minute))
			saved, _, err = st.GPTPayOrder(o.ID)
			if err != nil || saved.Status != "success" || saved.StatusCheckError != "" || reads.Load() != 2 {
				t.Fatalf("did not recover: %+v %v", saved, err)
			}
		})
	}
}

func TestGPTPayBackgroundBoundsAndBusyAccounts(t *testing.T) {
	var calls, inflight, peak atomic.Int32
	s, st, _ := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/status") {
			t.Error("background submitted purchase")
			return
		}
		calls.Add(1)
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		var input struct {
			IDs []string `json:"orderIds"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		time.Sleep(30 * time.Millisecond)
		fmt.Fprintf(w, `{"code":0,"data":{"orders":[{"id":%q,"status":"success","cancellationStatus":"failed"}]}}`, input.IDs[0])
	})
	cfg, _, _ := st.GPTPaySettings()
	cfg.CardAccountLimit = 100
	st.SaveGPTPaySettings(cfg, "")
	for i := 0; i < 24; i++ {
		seedGPTPayPollOrder(t, s, fmt.Sprint("order", i), fmt.Sprintf("poll%d@example.com", i), fmt.Sprint("remote", i))
	}
	seedGPTPayPollOrder(t, s, "unknown-no-id", "unknown@example.com", "")
	st.UpdateProAccount("poll0@example.com", func(p *model.MailAccountProfile) { p.ProAuto.Status = "running" })
	lock, _ := s.proLocks.LoadOrStore("account:poll1@example.com", &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.pollGPTPayOrders(context.Background(), time.Now()) }()
	}
	wg.Wait()
	lock.(*sync.Mutex).Unlock()
	if peak.Load() > 2 || calls.Load() > 20 || calls.Load() < 18 {
		t.Fatalf("unbounded/overlapping queries calls=%d peak=%d", calls.Load(), peak.Load())
	}
	for _, id := range []string{"order0", "order1", "unknown-no-id"} {
		o, _, _ := st.GPTPayOrder(id)
		if o.Status != "processing" {
			t.Fatal("touched busy/no-ID order")
		}
	}
}

func TestGPTPayCancellationTrackingAndTerminalProtection(t *testing.T) {
	var phase atomic.Int32
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch phase.Load() {
		case 0:
			fmt.Fprint(w, `{"code":0,"data":{"orders":[{"id":"remote-cancel","status":"success","cancellationStatus":"pending"}]}}`)
		case 1:
			fmt.Fprint(w, `{"code":0,"data":{"orders":[{"id":"remote-cancel","status":"processing","cancellationStatus":"waiting"}]}}`)
		default:
			fmt.Fprint(w, `{"code":0,"data":{"orders":[{"id":"remote-cancel","status":"success","cancellationStatus":"failed"}]}}`)
		}
	})
	o := seedGPTPayPollOrder(t, s, "cancel-order", email, "remote-cancel")
	s.pollGPTPayOrders(context.Background(), time.Now())
	saved, _, _ := st.GPTPayOrder(o.ID)
	p, _, _ := st.MailAccountCredential(email)
	if saved.Status != "success" || saved.NextStatusCheckAt == nil || p.ProManualStages["recharge"].Status != "completed" {
		t.Fatal("cancellation pending blocked completion")
	}
	phase.Store(1)
	stale := payOrder(t, payCall(t, s.refreshGPTPayOrder, "id", o.ID, map[string]any{}))
	if stale.Status != "success" || stale.Remote.CancellationStatus != "pending" {
		t.Fatal("terminal status regressed")
	}
	phase.Store(2)
	s.pollGPTPayOrders(context.Background(), time.Now().Add(2*time.Minute))
	saved, _, _ = st.GPTPayOrder(o.ID)
	if saved.Status != "success" || saved.Remote.CancellationStatus != "failed" || saved.NextStatusCheckAt != nil {
		t.Fatal("cancellation result not recorded separately")
	}
}
