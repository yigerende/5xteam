package httpapi

import (
	"context"
	"crypto/des"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

// Local-only issuer and payment fixtures. Never contact a real card platform.
func allocationFixture(t *testing.T) (*Server, *atomic.Int32, *atomic.Int32, string) {
	t.Helper()
	issued, paid := &atomic.Int32{}, &atomic.Int32{}
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/gpt-recharge/orders" {
			t.Errorf("unexpected payment request %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		n := paid.Add(1)
		fmt.Fprintf(w, `{"code":0,"data":{"id":"payment-%d","status":"success"}}`, n)
	})
	s.proAutoCancel()
	s.proAutoWG.Wait()
	configureProAutoFixture(t, s)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/authenticate" {
			fmt.Fprint(w, `{"code":"000000","data":{"authToken":"local-only","validity":3600000}}`)
			return
		}
		var env map[string]string
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		cipher, err := base64.StdEncoding.DecodeString(env["param"])
		if err != nil || len(cipher) == 0 || len(cipher)%8 != 0 {
			t.Error("bad envelope")
			w.WriteHeader(400)
			return
		}
		block, _ := des.NewCipher([]byte("fixture-"))
		plain := make([]byte, len(cipher))
		for i := 0; i < len(cipher); i += 8 {
			block.Decrypt(plain[i:i+8], cipher[i:i+8])
		}
		plain = plain[:len(plain)-int(plain[len(plain)-1])]
		var body struct {
			Method string        `json:"method"`
			Data   ejh.OpenInput `json:"data"`
		}
		if err = json.Unmarshal(plain, &body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if body.Method != "card.open.info" || body.Data.MCC != "trv" {
			t.Error("wrong issuer method/category")
			w.WriteHeader(400)
			return
		}
		n := issued.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"code": "000000", "data": ejh.Card{OrderNo: fmt.Sprintf("local-card-%d", n), Number: fmt.Sprintf("424242424242%04d", n), CVV: "123", Validity: "1299", Status: "1", CardCurrency: "USD"}})
	}))
	t.Cleanup(up.Close)
	s.ejh.client = ejh.New()
	s.ejh.client.HTTP = up.Client()
	cfg := ejh.DefaultSettings()
	cfg.Enabled = true
	cfg.URL = up.URL
	cfg.AppKey = "local-only"
	cfg.DefaultMaxAccounts = 4
	cfg.Templates = []ejh.Template{{ID: "allocation", Name: "local", ValidDays: 30, OpenInput: ejh.OpenInput{CardType: "fixture", CardAmount: "20", CardCurrency: "USD", BillCurrency: "USD", UseTime: 1, MCC: "trv"}}}
	if _, err := st.SaveEJHSettings(cfg, "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	pro, _, _, _ := st.ProSettings()
	pro.EJHAutoOpen = true
	pro.EJHTemplateID = "allocation"
	pro.EJHDailyOpenLimit = 20
	pro.EJHDailyAmountLimit = "400"
	pro.MaxUnmerged = 4
	pro.Concurrency = 4
	if _, err := st.SaveProSettings(pro, "", ""); err != nil {
		t.Fatal(err)
	}
	if rec := payCall(t, s.saveGPTPayCard, "id", "test-card", map[string]any{"card_type": "ejh", "max_accounts": 4}); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	// Reuse fixtures must carry opening-template evidence, just like real
	// issuer cards; a manually labelled card cannot bypass template admission.
	if err := st.EJHPut("operation", "resident-card-open", ejh.Operation{ID: "resident-card-open", Method: "open", TemplateID: "allocation", CardID: "test-card", Status: "completed", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	allocationAccount(t, s, "resident@example.com")
	_, secret, _ := st.GPTPayCard("test-card")
	if err := st.CreateGPTPayOrder(gptpay.Order{ID: "resident-order", Email: "resident@example.com", CardID: "test-card", Status: "success", CreatedAt: time.Now()}, gptpay.Snapshot{Input: gptpay.CreateInput{CardSecret: secret}}); err != nil {
		t.Fatal(err)
	}
	return s, issued, paid, email
}
func allocationAccount(t *testing.T, s *Server, email string) {
	t.Helper()
	if _, err := s.store.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: email, AccessToken: proAutoToken(email, "initial")}); err != nil {
		t.Fatal(err)
	}
}

func TestEJHAllocationSkipsNormalPendingAndCreatesNormal(t *testing.T) {
	s, issued, paid, email := allocationFixture(t)
	pending, err := s.store.ImportEJHCard(ejh.Card{OrderNo: "pending-card", Number: "5555555555554444", CVV: "123", Validity: "1299", Status: "1", CardCurrency: "USD"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.SetEJHCardNormalPending(pending.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.reserveProStartCard(context.Background(), email, pending.ID, true, false); err == nil || !strings.Contains(err.Error(), "正常待定") {
		t.Fatal("stale explicit selection accepted", err)
	}
	if issued.Load() != 0 || paid.Load() != 0 {
		t.Fatal("stale pending selection caused upstream mutation")
	}
	card, _, err := s.reserveProStartCard(context.Background(), email, "", true, false)
	if err != nil || card.ID == pending.ID || card.NormalPending || issued.Load() != 1 || paid.Load() != 0 {
		t.Fatal("automatic admission did not issue a normal replacement", err, issued.Load(), paid.Load())
	}
}

func TestEJHAllocationReviewHoldAndExistingAlternative(t *testing.T) {
	for _, status := range []string{"manual_review", "submission_unknown", "deleted", "failed"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", status, existing), func(t *testing.T) {
				s, issued, paid, email := allocationFixture(t)
				order, _, err := s.store.GPTPayOrder("resident-order")
				if err != nil {
					t.Fatal(err)
				}
				if status == "deleted" {
					if err = s.store.DeleteProAccount(order.Email); err != nil {
						t.Fatal(err)
					}
				} else {
					order.Status = status
					if err = s.store.UpdateGPTPayOrder(order); err != nil {
						t.Fatal(err)
					}
				}
				if existing {
					if _, err = s.store.SaveGPTPayCard(gptpay.Card{ID: "available-alternative", CardType: "ejh", Enabled: true}, gptpay.CardSecret{Number: "5555555555554444", Month: 12, Year: 2099, CVV: "123"}); err != nil {
						t.Fatal(err)
					}
				}
				card, _, err := s.reserveProStartCard(context.Background(), email, "test-card", true, false)
				wantIssued := int32(1)
				if existing || status == "failed" {
					wantIssued = 0
				}
				if err != nil || issued.Load() != wantIssued || paid.Load() != 0 {
					t.Fatal("unrelated occupancy blocked allocation", card.ID, err, issued.Load(), paid.Load())
				}
				if status == "failed" && card.ID != "test-card" || status != "failed" && card.ID == "test-card" || status != "failed" && existing && card.ID != "available-alternative" {
					t.Fatal("wrong card selected", card.ID)
				}
				saved, _, err := s.store.GPTPayOrder(order.ID)
				if err != nil || saved.Status != order.Status || saved.OccupancyResolution != nil {
					t.Fatal("old payment evidence changed", saved.Status, err)
				}
				if err = s.store.ReleaseProCard(email); err != nil {
					t.Fatal(err)
				}
				again, _, err := s.reserveProStartCard(context.Background(), email, "", true, false)
				if err != nil || again.ID != card.ID || issued.Load() != wantIssued || paid.Load() != 0 {
					t.Fatal("repeated evaluation duplicated issuance", again.ID, err, issued.Load())
				}
			})
		}
	}
}

func TestEJHNormalPendingOccupancyDoesNotBlockUnrelatedIssuance(t *testing.T) {
	for _, status := range []string{"deleted", "manual_review", "submission_unknown", "unused"} {
		t.Run(status, func(t *testing.T) {
			s, issued, paid, email := allocationFixture(t)
			remote := ejh.Card{OrderNo: "quarantined-card", Number: "5555555555554444", CVV: "123", Validity: "1299", Status: "3", CardCurrency: "USD"}
			pending, err := s.store.ImportEJHCard(remote, true)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err = s.store.EJHPut("operation", "quarantined-open", ejh.Operation{ID: "quarantined-open", Method: "open", CardID: pending.ID, OrderNo: remote.OrderNo, Status: "completed", Automatic: true, Amount: "20", Currency: "USD", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			owner := "quarantined@example.com"
			allocationAccount(t, s, owner)
			_, secret, err := s.store.GPTPayCard(pending.ID)
			if err != nil {
				t.Fatal(err)
			}
			paymentStatus := status
			if status == "deleted" {
				paymentStatus = "success"
			}
			order := gptpay.Order{ID: "quarantined-payment", Email: owner, CardID: pending.ID, Status: paymentStatus, CreatedAt: time.Now()}
			if status != "unused" {
				if err = s.store.CreateGPTPayOrder(order, gptpay.Snapshot{Input: gptpay.CreateInput{CardSecret: secret}}); err != nil {
					t.Fatal(err)
				}
			}
			if status == "deleted" {
				if err = s.store.EJHMarkRequested(owner, "manual", time.Now()); err != nil {
					t.Fatal(err)
				}
				if err = s.store.DeleteProAccount(owner); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.store.SetEJHCardNormalPending(pending.ID, true); err != nil {
				t.Fatal(err)
			}
			before, _, err := s.store.GPTPayCard(pending.ID)
			wantOccupants := 1
			if status == "unused" {
				wantOccupants = 0
			}
			if err != nil || !before.NormalPending || before.Available || len(before.Occupants) != wantOccupants {
				t.Fatal("fixture must retain quarantined occupancy", before, err)
			}
			if _, _, err = s.reserveProStartCard(context.Background(), email, pending.ID, true, false); err == nil || !strings.Contains(err.Error(), "正常待定") {
				t.Fatal("explicit pending selection was accepted", err)
			}
			card, _, err := s.reserveProStartCard(context.Background(), email, "", true, false)
			if err != nil || card.ID == pending.ID || card.ID == "test-card" || issued.Load() != 1 || paid.Load() != 0 {
				t.Fatal("quarantined history blocked unrelated issuance", card.ID, err, issued.Load(), paid.Load())
			}
			after, _, err := s.store.GPTPayCard(pending.ID)
			if err != nil || !after.NormalPending || after.Available || len(after.Occupants) != wantOccupants || (wantOccupants > 0 && after.Occupants[0] != before.Occupants[0]) {
				t.Fatal("quarantined occupancy was changed", after, err)
			}
			if status != "unused" {
				saved, _, err := s.store.GPTPayOrder(order.ID)
				if err != nil || saved.Status != paymentStatus || saved.OccupancyResolution != nil {
					t.Fatal("original payment was resolved or rewritten", saved.Status, err)
				}
			}
			if err = s.store.ReleaseProCard(email); err != nil {
				t.Fatal(err)
			}
			again, _, err := s.reserveProStartCard(context.Background(), email, "", true, false)
			if err != nil || again.ID != card.ID || issued.Load() != 1 || paid.Load() != 0 {
				t.Fatal("repeated evaluation issued another card", again.ID, err)
			}
		})
	}
}

func TestEJHAllocationManualFallbackAndReplay(t *testing.T) {
	for _, selected := range []string{"", "test-card"} {
		t.Run("selected-"+selected, func(t *testing.T) {
			s, issued, paid, email := allocationFixture(t)
			input := payInput("allocation-request-01")
			input["card_id"] = selected
			o := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, input))
			if o.Status != "success" || o.CardID == "test-card" || issued.Load() != 1 || paid.Load() != 1 {
				t.Fatal(o, issued.Load(), paid.Load())
			}
			original, _, _ := s.store.GPTPayCard("test-card")
			if original.AccountLimit != 4 || original.OpenedAccounts != 1 || original.RemainingAccounts != 3 || original.ActiveAccounts != 1 {
				t.Fatal(original)
			}
			again := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, input))
			if again.ID != o.ID || issued.Load() != 1 || paid.Load() != 1 {
				t.Fatal("duplicate purchase")
			}
			input["card_id"] = "different"
			if rec := payCall(t, s.createGPTPayOrder, "email", email, input); rec.Code != 409 {
				t.Fatal("idempotency parameters changed", rec.Body.String())
			}
		})
	}
}

func TestEJHAllocationConcurrentManualAndReleasedReuse(t *testing.T) {
	s, issued, paid, _ := allocationFixture(t)
	const count = 6
	for i := 0; i < count; i++ {
		allocationAccount(t, s, fmt.Sprintf("parallel-%d@example.com", i))
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- payCall(t, s.createGPTPayOrder, "email", fmt.Sprintf("parallel-%d@example.com", i), payInput(fmt.Sprintf("parallel-allocation-%d", i)))
		}(i)
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for rec := range results {
		o := payOrder(t, rec)
		if o.Status != "success" || seen[o.CardID] {
			t.Fatal("duplicate occupied card", o)
		}
		seen[o.CardID] = true
		card, _, _ := s.store.GPTPayCard(o.CardID)
		if card.ActiveAccounts != 1 || card.AccountLimit != 4 || card.PendingAccounts != 0 {
			t.Fatal(card)
		}
	}
	if issued.Load() != count || paid.Load() != count {
		t.Fatal(issued.Load(), paid.Load())
	}
	if _, err := s.store.UpdateProAccount("resident@example.com", func(p *model.MailAccountProfile) { p.RemoveStatus = "completed" }); err != nil {
		t.Fatal(err)
	}
	ejhReceiveOrderAPI(t, s, "test-card", "resident-order")
	for i := 0; i < 4; i++ {
		email := fmt.Sprintf("reuse-%d@example.com", i)
		allocationAccount(t, s, email)
		input := payInput(fmt.Sprintf("reuse-allocation-%d", i))
		input["card_id"] = ""
		o := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, input))
		if i < 3 && (o.CardID != "test-card" || issued.Load() != count) {
			t.Fatal("free card not preferred", o, issued.Load())
		}
		if i == 3 && (o.CardID == "test-card" || issued.Load() != count+1) {
			t.Fatal("lifetime cap ignored", o, issued.Load())
		}
		if _, err := s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.RemoveStatus = "completed" }); err != nil {
			t.Fatal(err)
		}
		ejhReceiveOrderAPI(t, s, o.CardID, o.ID)
	}
	original, _, _ := s.store.GPTPayCard("test-card")
	if original.OpenedAccounts != 4 || original.Available {
		t.Fatal(original)
	}
}

func TestEJHAllocationGuardsBeforeIssuance(t *testing.T) {
	for _, kind := range []string{"disabled", "budget", "maximum", "unknown", "invalid-session", "switch-off"} {
		t.Run(kind, func(t *testing.T) {
			s, issued, paid, email := allocationFixture(t)
			pro, _, _, _ := s.store.ProSettings()
			input := payInput("guard-allocation-01")
			switch kind {
			case "disabled":
				pro.EJHAutoOpen = false
			case "budget":
				pro.EJHDailyAmountLimit = "19.99"
			case "maximum":
				pro.MaxUnmerged = 1
			case "unknown":
				if err := s.store.EJHPut("operation", "unknown", ejh.Operation{ID: "unknown", Method: "open", Status: "unknown"}); err != nil {
					t.Fatal(err)
				}
			case "invalid-session":
				p, c, _ := s.store.MailAccountCredential(email)
				c.AccessToken = ""
				if _, err := s.store.SaveMailAccount(p, c); err != nil {
					t.Fatal(err)
				}
			case "switch-off":
				cfg, _, _ := s.store.EJHSettings()
				cfg.SingleActiveAccount = false
				if _, err := s.store.SaveEJHSettings(cfg, ""); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.store.SaveProSettings(pro, "", ""); err != nil {
				t.Fatal(err)
			}
			if kind == "maximum" {
				_, _, err := s.reserveProStartCard(context.Background(), email, "", true, true)
				if err == nil {
					t.Fatal("maximum ignored")
				}
			} else {
				rec := payCall(t, s.createGPTPayOrder, "email", email, input)
				if kind == "switch-off" {
					o := payOrder(t, rec)
					if o.CardID != "test-card" {
						t.Fatal(o)
					}
				} else if rec.Code < 400 {
					t.Fatal("guard ignored", rec.Body.String())
				}
			}
			if issued.Load() != 0 {
				t.Fatal("guard bought card", issued.Load())
			}
			if kind != "switch-off" && paid.Load() != 0 {
				t.Fatal("guard charged payment")
			}
		})
	}
}

func TestEJHAllocationAutoAndScheduledStarts(t *testing.T) {
	for _, mode := range []string{"auto", "timer", "manual-tick"} {
		t.Run(mode, func(t *testing.T) {
			s, issued, _, first := allocationFixture(t)
			emails := []string{first, "next-one@example.com", "next-two@example.com"}
			for _, email := range emails[1:] {
				allocationAccount(t, s, email)
			}
			gate := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(gate) })
			s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, email string, rpc func(string, map[string]any) (any, error)) error {
				select {
				case <-gate:
				case <-ctx.Done():
					return ctx.Err()
				}
				if _, err := rpc("pro_recharge", map[string]any{"session": map[string]any{"access_token": proAutoToken(email, "initial"), "user": map[string]any{"email": email}, "account": map[string]any{"id": "personal-id"}}}); err != nil {
					return err
				}
				_, err := rpc("pro_tokens", map[string]any{"tokens": map[string]any{"access_token": proAutoToken(email, "after"), "refresh_token": "local-only"}})
				return err
			}, Push: func(ctx context.Context, email string) (model.MailAccountProfile, error) {
				return s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
					p.PushStatus = "completed"
					p.PushProvider = "sub2"
					p.Sub2AccountID = 1
				})
			}, Quota: func(ctx context.Context, email string, _ bool) (model.MailAccountProfile, error) {
				return s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
					p.QuotaStatus = "completed"
					p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 1}
				})
			}, Merge: func(context.Context, string) (model.MailAccountProfile, error) {
				t.Error("unexpected merge")
				return model.MailAccountProfile{}, fmt.Errorf("unexpected merge")
			}}
			if mode == "auto" {
				var wg sync.WaitGroup
				for _, email := range emails {
					wg.Add(1)
					go func(email string) {
						defer wg.Done()
						rec := payCall(t, s.startProAutomation, "email", email, map[string]any{"card_id": "test-card", "plan_code": "pro20"})
						if rec.Code != 202 {
							t.Error(rec.Code, rec.Body.String())
						}
					}(email)
				}
				wg.Wait()
			} else {
				pro, _, _, _ := s.store.ProSettings()
				pro.ScheduledEnabled = true
				if _, err := s.store.SaveProSettings(pro, "", ""); err != nil {
					t.Fatal(err)
				}
				s.tickProSchedule(mode == "manual-tick")
				runs, _, err := s.store.ProScheduleRuns(10, 0)
				if err != nil || len(runs) != 1 || len(runs[0].Tasks) != 3 {
					t.Fatal(runs, err)
				}
				for _, task := range runs[0].Tasks {
					if task.CardID == "" || task.Status != "running" {
						t.Fatal(task)
					}
				}
			}
			if issued.Load() != 3 {
				t.Fatal("did not replenish each occupied slot", issued.Load())
			}
			cards := map[string]bool{}
			for _, email := range emails {
				p, _, _ := s.store.MailAccountCredential(email)
				if p.ProAuto.CardID == "test-card" || cards[p.ProAuto.CardID] {
					t.Fatal(p.ProAuto)
				}
				cards[p.ProAuto.CardID] = true
			}
			once.Do(func() { close(gate) })
			for _, email := range emails {
				p := waitProAutoFinished(t, s, email)
				if p.ProAuto.Status != "waiting_quota" {
					t.Fatal(p.ProAuto)
				}
			}
			if mode != "auto" {
				if err := s.store.SetProScheduleNext(time.Now().Add(-time.Second)); err != nil {
					t.Fatal(err)
				}
				s.tickProSchedule(false)
				runs, _, _ := s.store.ProScheduleRuns(10, 0)
				if runs[0].Status != "skipped" || !strings.Contains(runs[0].Message, "达到上限") || issued.Load() != 3 {
					t.Fatal(runs, issued.Load())
				}
			}
		})
	}
}
