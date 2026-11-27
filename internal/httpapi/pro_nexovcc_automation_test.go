package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/nexovcc"
	"chatgpt-space-merge/internal/restock"
)

func TestProConfiguredNexovccAllAutomaticEntrypoints(t *testing.T) {
	for _, entry := range []string{"manual", "schedule", "restock"} {
		for _, autoOpen := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/auto_open_%v", entry, autoOpen), func(t *testing.T) {
				var payments, pushes atomic.Int32
				s, f, email, c := restockWindowIntegration(t, func(w http.ResponseWriter, r *http.Request) {
					payments.Add(1)
					fmt.Fprint(w, `{"code":0,"data":{"id":"configured-paid","status":"success"}}`)
				})

				var detailReads atomic.Int32
				if autoOpen {
					transport := s.nexovcc.transport
					s.nexovcc.transport = func(ctx context.Context, r nexovcc.Request) (nexovcc.Response, error) {
						if r.Path == "/api/card/getUserCardDetail" && detailReads.Add(1) == 1 {
							return nexReply(map[string]any{"serial_no": r.Body["serial_no"], "status": "1", "expire_at_str": ""}), nil
						}
						return transport(ctx, r)
					}
				}
				p, _, _, _ := s.store.ProSettings()
				p.CardProvider, p.NexovccAutoOpen = "nexovcc", autoOpen
				p.MaxUnmerged, p.Concurrency = 1, 1
				if _, err := s.store.SaveProSettings(p, "", ""); err != nil {
					t.Fatal(err)
				}
				c.NoRestockEnabled = false // Exercise the global issuer, without a window override.
				if _, err := s.store.SaveProRestockConfig(c); err != nil {
					t.Fatal(err)
				}
				cardID := ""
				if !autoOpen {
					card, err := s.store.SaveNexovccCard(nexovcc.Card{Serial: "existing-global", Status: "1", Balance: "10.00", CreatedAt: time.Now().Add(-time.Hour)}, &nexovcc.PrivateCard{Number: "5555555555554444", CVV: "123", Month: 12, Year: 2099})
					if err != nil {
						t.Fatal(err)
					}
					cardID = card.ID
				}
				s.proAutoHooks = &proAutomationHooks{
					Session: func(ctx context.Context, current string, rpc func(string, map[string]any) (any, error)) error {
						profile, _, _ := s.store.MailAccountCredential(current)
						card, _, err := s.store.GPTPayCard(profile.ProAuto.CardID)
						if current != email || err != nil || card.Source != "nexovcc" || (!autoOpen && card.ID != cardID) {
							t.Error("automatic entry chose the wrong account/card", current, card, err)
						}
						if err = windowRecharge(s, current, rpc); err != nil {
							return err
						}
						_, err = rpc("pro_tokens", map[string]any{"tokens": map[string]any{"access_token": proAutoToken(current, "new"), "refresh_token": "fixture-rt"}})
						return err
					},
					Push: func(_ context.Context, current string) (model.MailAccountProfile, error) {
						pushes.Add(1)
						return s.store.UpdateProAccount(current, func(p *model.MailAccountProfile) {
							p.PushStatus, p.PushProvider, p.Sub2AccountID = "completed", "sub2", 88
						})
					},
					Quota: func(_ context.Context, current string, _ bool) (model.MailAccountProfile, error) {
						return s.store.UpdateProAccount(current, func(p *model.MailAccountProfile) { p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 10} })
					},
				}
				switch entry {
				case "manual":
					pay, _, _ := s.store.GPTPaySettings()
					w := payCall(t, s.startProAutomation, "email", email, map[string]any{"provider": pay.Provider, "plan_code": pay.PlanCode, "card_id": cardID})
					if w.Code != http.StatusAccepted {
						t.Fatal(w.Code, w.Body.String())
					}
				case "schedule":
					s.tickProSchedule(true)
				case "restock":
					windowTick(t, s)
				}
				profile := waitProAutoFinished(t, s, email)
				wantOpens := int32(0)
				if autoOpen {
					wantOpens = 1
				}
				if payments.Load() != 1 || pushes.Load() != 1 || f.opens.Load() != wantOpens || profile.ProAuto.Status != "waiting_quota" {
					t.Fatal("configured issuer workflow", profile.ProAuto, payments.Load(), pushes.Load(), f.opens.Load())
				}
				if entry == "restock" {
					windowTick(t, s)
					tasks, _ := s.store.ProRestockTasks()
					if len(tasks) != 1 || tasks[0].Status != "verifying" || tasks[0].CardProvider != "nexovcc" || tasks[0].StrictCardProvider {
						t.Fatal("global default did not wait for monitor", tasks)
					}
					collect := s.proRestock.monitor
					s.proRestock.monitor = func(ctx context.Context, c restock.Config, p model.ProSettings, password string) (restock.Snapshot, error) {
						snap, err := collect(ctx, c, p, password)
						snap.Accounts = append(snap.Accounts, restock.Account{ID: 88, Ready: true, Capacity: 10})
						return snap, err
					}
					windowTick(t, s)
					tasks, _ = s.store.ProRestockTasks()
					if tasks[0].Status != "ready" || tasks[0].ReadyAt == nil {
						t.Fatal("monitor did not release completed delivery", tasks)
					}
				}
			})
		}
	}
}

func TestProNexovccRestockCanRecoverBeforeWorkflowCreated(t *testing.T) {
	for _, mode := range []string{"budget", "import_pending"} {
		t.Run(mode, func(t *testing.T) {
			s, f, email, c := restockWindowIntegration(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected payment") })
			p, _, _, _ := s.store.ProSettings()
			p.CardProvider, p.RetryCount = "nexovcc", 0
			if mode == "budget" {
				p.NexovccDailyAmountLimit = "9.99"
			} else {
				f.failDetail = true
			}
			if _, err := s.store.SaveProSettings(p, "", ""); err != nil {
				t.Fatal(err)
			}
			c.NoRestockEnabled = false
			if _, err := s.store.SaveProRestockConfig(c); err != nil {
				t.Fatal(err)
			}
			s.proAutoHooks = &proAutomationHooks{Session: func(context.Context, string, func(string, map[string]any) (any, error)) error {
				return errors.New("fixture stops after safe startup")
			}}
			windowTick(t, s)
			waitRestockPreparationIdle(t, s)
			tasks, _ := s.store.ProRestockTasks()
			wantStatus := "failed"
			if mode == "import_pending" {
				wantStatus = "waiting_card"
			}
			if len(tasks) != 1 || tasks[0].Status != wantStatus {
				t.Fatal("startup not preserved", tasks)
			}
			if mode == "import_pending" {
				// Current issuance is automatically resumed in waiting_card. This
				// regression covers legacy unbound operations that required retry;
				// current recovery is covered by the card-import integration tests.
				ops, err := s.store.NexovccOperations()
				if err != nil || len(ops) != 1 || ops[0].RestockTaskID != tasks[0].ID {
					t.Fatal("issuance lost its task", ops, err)
				}
				ops[0].RestockTaskID = ""
				if err := s.store.SaveNexovccOperation(ops[0]); err != nil {
					t.Fatal(err)
				}
				tasks[0].Status = "attention"
			}
			// Reproduce a persisted scheduled task that failed before ProAuto was
			// created. Its extra groups must survive both detachment and retries.
			tasks[0].PurchaseRoute = &model.ProPurchaseRoute{Sub2GroupIDs: []int64{1, 15}}
			if err := s.store.SaveProRestockTask(tasks[0]); err != nil {
				t.Fatal(err)
			}
			message := tasks[0].Message
			windowTick(t, s)
			waitRestockPreparationIdle(t, s)
			tasks, _ = s.store.ProRestockTasks()
			if mode == "budget" {
				if tasks[0].Status != "failed" || !strings.Contains(tasks[0].Message, message) {
					t.Fatal("safe preparation failure was not released with its original cause", tasks)
				}
				message = tasks[0].Message
			} else if !tasks[0].AutoIsolated || tasks[0].Active() || !strings.Contains(tasks[0].Message, message) {
				t.Fatal("startup cause overwritten", tasks[0].Message, message)
			}
			message = tasks[0].Message
			r := httptest.NewRequest("GET", "/", nil)
			r.SetPathValue("email", email)
			w := httptest.NewRecorder()
			s.getProAutomation(w, r)
			var response struct {
				Data struct {
					Retry bool   `json:"restock_retry_available"`
					Error string `json:"error"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || !response.Data.Retry || response.Data.Error != message {
				t.Fatal("recovery action or error missing", w.Body.String(), err)
			}
			pay, _, _ := s.store.GPTPaySettings()
			retry := func() *httptest.ResponseRecorder {
				return payCall(t, s.startProAutomation, "email", email, map[string]any{"provider": pay.Provider, "plan_code": pay.PlanCode, "card_id": "", "retry_current": true})
			}
			if mode == "import_pending" {
				if w = retry(); w.Code != 409 || !strings.Contains(w.Body.String(), "尚未核实") {
					t.Fatal("unverified issuance permitted retry", w.Code, w.Body.String())
				}
				profile, _, _ := s.store.MailAccountCredential(email)
				if profile.ProRestockTaskID != tasks[0].ID || f.opens.Load() != 1 {
					t.Fatal("unsafe retry detached the task or issued again")
				}
				ops, _ := s.store.NexovccOperations()
				f.failDetail = false
				op, err := s.resolveNexovccLocked(context.Background(), nexovccInput{RequestID: ops[0].ID, Outcome: "query"})
				if err != nil || op.Status != "succeeded" {
					t.Fatal("original card recovery", op, err)
				}
			} else {
				if w = retry(); w.Code != 409 {
					t.Fatal("unchanged budget should still block opening", w.Code, w.Body.String())
				}
				profile, _, _ := s.store.MailAccountCredential(email)
				if profile.ProRestockTaskID != "" || !slices.Equal(profile.ProAuto.Sub2PushGroupIDs, []int64{1, 15}) {
					t.Fatal("second startup failure discarded original push groups", profile.ProAuto)
				}
				p.NexovccDailyAmountLimit = "10.00"
				if _, err := s.store.SaveProSettings(p, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			if w = retry(); w.Code != 202 {
				t.Fatal("resolved startup could not continue", w.Code, w.Body.String())
			}
			profile := waitProAutoFinished(t, s, email)
			if !slices.Equal(profile.ProAuto.Sub2PushGroupIDs, []int64{1, 15}) {
				t.Fatal("manual retry lost the restock task's extra push groups", profile.ProAuto)
			}
			card, _, err := s.store.GPTPayCard(profile.ProAuto.CardID)
			if err != nil || card.Source != "nexovcc" || profile.ProAuto.ID == "" || profile.ProRestockTaskID != "" || f.opens.Load() != 1 {
				t.Fatal("retry lost account or original issued card", profile.ProAuto, err, f.opens.Load())
			}
		})
	}
}
