package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"chatgpt-space-merge/internal/ejh"
	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/store"
)

// Exercise both user-facing full-auto entrypoints with matching-template
// pending/closing/closed cards and a usable card of the opposite plan.
func TestEJHNormalTemplateFullAutoEntrypoints(t *testing.T) {
	for _, mode := range []string{"manual", "restock"} {
		for _, plan := range []string{"pro5", "pro20"} {
			for _, existing := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/existing=%t", mode, plan, existing), func(t *testing.T) {
					s, email, opposite, target := manualTemplateFixture(t, "ejh", plan)
					st := s.store
					p, _, _, _ := st.ProSettings()
					p.PurchaseScheduleEnabled = false
					p.MaxUnmerged = 20
					if _, err := st.SaveProSettings(p, "", ""); err != nil {
						t.Fatal(err)
					}
					history := make(map[string]gptpay.Card)
					for i, status := range []string{"3", "8", "2"} {
						id := fmt.Sprintf("quarantined-%d", i)
						remote := ejh.Card{OrderNo: id, Number: fmt.Sprintf("55555555555599%02d", i), CVV: "123", Validity: "1299", Status: "1", CardCurrency: "USD"}
						card, err := st.ImportEJHCard(remote, true)
						if err != nil {
							t.Fatal(err)
						}
						now := time.Now()
						op := ejh.Operation{ID: id, Method: "open", TemplateID: target, PlanCode: plan, CardID: card.ID, Status: "completed", Automatic: true, Amount: "20", Currency: "USD", CreatedAt: now, UpdatedAt: now}
						if err = st.EJHPut("operation", id, op); err != nil {
							t.Fatal(err)
						}
						owner := id + "@example.com"
						allocationAccount(t, s, owner)
						_, secret, _ := st.GPTPayCard(card.ID)
						payment := []string{"success", "submission_unknown", "manual_review"}[i]
						if err = st.CreateGPTPayOrder(gptpay.Order{ID: id, Email: owner, CardID: card.ID, Status: payment, CreatedAt: now}, gptpay.Snapshot{Input: gptpay.CreateInput{CardSecret: secret}}); err != nil {
							t.Fatal(err)
						}
						if err = st.DeleteMailAccountFromManagement(owner); err != nil {
							t.Fatal(err)
						}
						remote.Status = status
						if _, err = st.ImportEJHCard(remote, false); err != nil {
							t.Fatal(err)
						}
						if i == 0 {
							if _, err = st.SetEJHCardNormalPending(card.ID, true); err != nil {
								t.Fatal(err)
							}
						}
						card, _, err = st.GPTPayCard(card.ID)
						if err != nil || card.Available || len(card.Occupants) != 1 {
							t.Fatal("history must remain unavailable and occupied", card.ID, err)
						}
						history[card.ID] = card
					}
					var expectedCard string
					if existing {
						ctx := context.WithValue(context.Background(), proPurchaseContextKey{}, &model.ProPurchaseRoute{CardProvider: "ejh", TemplateID: target, PlanCode: plan})
						card, err := s.ensureProviderCard(ctx)
						if err != nil {
							t.Fatal(err)
						}
						expectedCard = card.ID
					}
					before, err := store.EJHRecords[ejh.Operation](st, "operation")
					if err != nil {
						t.Fatal(err)
					}
					gate, resume := context.WithCancel(context.Background())
					defer resume()
					started := make(chan struct{}, 2)
					s.proAutoHooks = &proAutomationHooks{Session: func(ctx context.Context, current string, rpc func(string, map[string]any) (any, error)) error {
						started <- struct{}{}
						<-gate.Done()
						if err := windowRecharge(s, current, rpc); err != nil {
							return err
						}
						_, err := rpc("pro_tokens", map[string]any{"tokens": map[string]any{"access_token": proAutoToken(current, "paid"), "refresh_token": "fixture-rt"}})
						return err
					}, Push: func(_ context.Context, current string) (model.MailAccountProfile, error) {
						return st.UpdateProAccount(current, func(p *model.MailAccountProfile) { p.PushStatus = "completed" })
					}, Quota: func(context.Context, string, bool) (model.MailAccountProfile, error) {
						return model.MailAccountProfile{}, errors.New("fixture stops at quota")
					}}
					if mode == "restock" {
						c := restock.DefaultConfig()
						c.Enabled, c.Mode, c.GroupID, c.EmptyGroupCount = true, "auto", 1, 1
						if _, err := st.SaveProRestockConfig(c); err != nil {
							t.Fatal(err)
						}
						pay, _, _ := st.GPTPaySettings()
						s.proRestock.monitor = func(context.Context, restock.Config, model.ProSettings, string) (restock.Snapshot, error) {
							return restock.Snapshot{At: time.Now(), Valid: true, LoadValid: true, GroupID: 1, Binding: restockBinding(p), NewPlan: restockPlan(pay.Provider, pay.PlanCode, pay.Country, pay.CreditQuantity)}, nil
						}
					}
					trigger := func() {
						if mode == "manual" {
							r := payCall(t, s.startProAutomation, "email", email, map[string]any{"provider": "tokenseek", "plan_code": plan, "card_provider": "ejh"})
							if r.Code != http.StatusAccepted {
								t.Fatal("manual start failed", r.Body.String())
							}
						} else if err := s.tickProRestock(context.Background(), true); err != nil {
							t.Fatal(err)
						}
					}
					trigger()
					select {
					case <-started:
					case <-time.After(5 * time.Second):
						t.Fatal("card allocation never reached login")
					}
					trigger()
					resume()
					profile := waitProAutoFinished(t, s, email)
					if profile.ProAuto.Status != "waiting_quota" {
						t.Fatal("full-auto flow failed", profile.ProAuto)
					}
					orders, _, err := st.GPTPayOrders(20, 0, email)
					if err != nil || len(orders) != 1 || orders[0].Status != "success" || orders[0].PlanCode != plan || orders[0].CardID == opposite.ID {
						t.Fatal("payment failed, duplicated or used opposite plan", orders, err)
					}
					matches, err := st.ProTemplateCards("ejh", target, plan)
					if err != nil || !matches[orders[0].CardID] || (existing && orders[0].CardID != expectedCard) {
						t.Fatal("did not use the matching normal card", orders[0].CardID, err)
					}
					after, err := store.EJHRecords[ejh.Operation](st, "operation")
					wantNew := 1
					if existing {
						wantNew = 0
					}
					if err != nil || len(after)-len(before) != wantNew {
						t.Fatal("opening was skipped or duplicated", len(before), len(after), err)
					}
					for id, old := range history {
						card, _, err := st.GPTPayCard(id)
						if err != nil || card.Available || card.NormalPending != old.NormalPending || card.IssuerStatus != old.IssuerStatus || !reflect.DeepEqual(card.Occupants, old.Occupants) {
							t.Fatal("quarantined card or occupancy changed", id, err)
						}
					}
					card, _, err := st.GPTPayCard(opposite.ID)
					if err != nil || card.ActiveAccounts != 0 || card.PendingAccounts != 0 {
						t.Fatal("opposite plan card was occupied", err)
					}
				})
			}
		}
	}
}
