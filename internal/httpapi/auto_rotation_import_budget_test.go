package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func saveImportLimitMail(t *testing.T, st *store.Store, email string) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth":    map[string]string{"chatgpt_user_id": email, "chatgpt_account_id": email, "chatgpt_plan_type": "free"},
		"https://api.openai.com/profile": map[string]string{"email": email},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: token}); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestAutoRotationImportBudgetSurvivesPreparationFailures(t *testing.T) {
	for _, tc := range []struct {
		name, failure                              string
		seats, max, waiting, concurrency, wantMail int
		reuse                                      bool
	}{
		{"two_seats_twenty_mailboxes", "claim", 2, 10, 0, 3, 2, false},
		{"max_per_run_is_smaller", "claim", 10, 2, 0, 3, 2, false},
		{"unlimited_max_still_respects_seats", "claim", 2, 0, 0, 3, 2, false},
		{"waiting_account_leaves_one_mail_slot", "claim", 2, 10, 1, 3, 1, false},
		{"waiting_accounts_fill_budget", "claim", 2, 10, 2, 3, 0, false},
		{"no_seats_no_import", "claim", 0, 10, 0, 3, 0, false},
		{"serial_execution", "claim", 2, 10, 0, 1, 2, false},
		{"high_concurrency", "claim", 2, 10, 0, 20, 2, false},
		{"high_concurrency_with_tasks", "", 2, 10, 0, 20, 2, false},
		{"concurrent_waiting_and_mail_tasks", "", 4, 10, 2, 20, 2, false},
		{"waiting_list_exceeds_budget", "", 2, 10, 5, 20, 0, false},
		{"reuse_enabled", "claim", 2, 10, 0, 3, 2, true},
		{"used_history_rejected_inside_mail_loop", "used", 2, 10, 0, 3, 2, false},
		{"restart_restores_pending_accounts", "pending", 2, 10, 0, 3, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			// Missing Team ID stops any valid waiting task before remote requests.
			admin := saveTestAdmin(t, st, model.AdminAccountProfile{Email: "mother@example.com"}, "fixture-token", "")
			if err := st.SaveAdminCapacitySnapshot(admin.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: tc.seats}}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tc.waiting; i++ {
				email := fmt.Sprintf("waiting-%02d@example.com", i)
				if _, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: email}, "fixture-token"); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 20; i++ {
				email := fmt.Sprintf("mail-%02d@example.com", i)
				token := saveImportLimitMail(t, st, email)
				if tc.failure == "" {
					continue
				}
				// Pending history consumes both seats after reimport; subsequent
				// fresh candidates then fail mother selection in the old loop.
				if tc.failure == "pending" && i >= 2 {
					continue
				}
				profile, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: email}, token)
				if err != nil {
					t.Fatal(err)
				}
				profile, err = st.UpdateFreeAccount(profile.ID, func(p *model.FreeAccountProfile) {
					switch tc.failure {
					case "claim":
						p.HistoryUncertain = true
					case "used":
						p.TeamAccountID, p.AcceptStatus, p.RemoveStatus = "previous-team", "completed", "completed"
					case "pending":
						p.AdminAccountID, p.TeamAccountID, p.SeatType = admin.ID, "pending-team", "prolite"
						p.JoinMethod, p.InviteStatus, p.AcceptStatus = "child_request", "running", "pending"
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				if tc.failure == "pending" {
					if err := st.SaveAutoRotationTask(model.AutoRotationTask{ID: fmt.Sprintf("old-%d", i), RunID: "old", AccountID: profile.ID, CycleID: profile.CycleID, AdminAccountID: admin.ID, SeatType: "prolite", Status: "running"}); err != nil {
						t.Fatal(err)
					}
				}
				// Removing a list row intentionally leaves its durable history.
				if err := st.DeleteFreeAccount(profile.ID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failure == "pending" {
				if err := st.RecoverAutoRotationClaims(); err != nil {
					t.Fatal(err)
				}
				if err := st.RecoverAutoRotationTasks(); err != nil {
					t.Fatal(err)
				}
			}
			run := model.AutoRotationRun{ID: "import-budget", Status: "running", StartedAt: time.Now()}
			if err := st.SaveAutoRotationRun(run); err != nil {
				t.Fatal(err)
			}
			s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 1000)}
			s.executeAutoRotation(context.Background(), run, model.AutoRotationSettings{
				MaxPerRun: tc.max, Concurrency: tc.concurrency, AllowMultiMotherReuse: tc.reuse, JoinMethod: "child_request",
			})
			imported := make(map[string]bool)
			for _, account := range st.FreeAccounts() {
				if strings.HasPrefix(account.Email, "mail-") {
					imported[account.Email] = true
				}
			}
			if len(imported) != tc.wantMail {
				t.Fatalf("mail imports=%d, want %d (seats=%d max=%d waiting=%d tasks=%d)", len(imported), tc.wantMail, tc.seats, tc.max, tc.waiting, len(st.AutoRotationTasks(run.ID)))
			}
			for i := 0; i < 20; i++ {
				email := fmt.Sprintf("mail-%02d@example.com", i)
				if imported[email] != (i < tc.wantMail) {
					t.Fatalf("oldest-first import mismatch for %s", email)
				}
			}
			wantTasks := tc.waiting
			if tc.failure == "" {
				wantTasks += tc.wantMail
			}
			if wantTasks > tc.seats {
				wantTasks = tc.seats
			}
			if tc.max > 0 && wantTasks > tc.max {
				wantTasks = tc.max
			}
			if tasks := st.AutoRotationTasks(run.ID); len(tasks) != wantTasks {
				t.Fatalf("execution tasks=%d, want %d", len(tasks), wantTasks)
			}
		})
	}
}

func TestAutoRotationImportBudgetSkipsUnusableWaitingAndMissingAT(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admin := saveTestAdmin(t, st, model.AdminAccountProfile{Email: "mother@example.com"}, "fixture-token", "")
	if err := st.SaveAdminCapacitySnapshot(admin.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: 2}}); err != nil {
		t.Fatal(err)
	}
	blocked, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "blocked@example.com", UserID: "blocked"}, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateFreeAccount(blocked.ID, func(p *model.FreeAccountProfile) { p.InviteStatus = "running" }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: "missing@example.com"}, model.MailAccountCredentials{Email: "missing@example.com"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		saveImportLimitMail(t, st, fmt.Sprintf("mail-%02d@example.com", i))
	}
	run := model.AutoRotationRun{ID: "usable-budget", Status: "running", StartedAt: time.Now()}
	if err := st.SaveAutoRotationRun(run); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 1000)}
	s.executeAutoRotation(context.Background(), run, model.AutoRotationSettings{MaxPerRun: 10, Concurrency: 3})
	if accounts := st.FreeAccounts(); len(accounts) != 3 {
		t.Fatalf("expected existing blocked row plus two imported mailboxes, got %d", len(accounts))
	}
	if tasks := st.AutoRotationTasks(run.ID); len(tasks) != 2 {
		t.Fatalf("expected two eligible replacements, got %d", len(tasks))
	}
}

func TestAutoRotationImportBudgetCancelledBeforeSelection(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admin := saveTestAdmin(t, st, model.AdminAccountProfile{Email: "mother@example.com"}, "fixture-token", "")
	if err := st.SaveAdminCapacitySnapshot(admin.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: 2}}); err != nil {
		t.Fatal(err)
	}
	saveImportLimitMail(t, st, "mail@example.com")
	run := model.AutoRotationRun{ID: "cancelled-budget", Status: "running", StartedAt: time.Now()}
	if err := st.SaveAutoRotationRun(run); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, auditQueue: make(chan model.AutoRotationEvent, 100)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.executeAutoRotation(ctx, run, model.AutoRotationSettings{MaxPerRun: 2, Concurrency: 3})
	if count := len(st.FreeAccounts()); count != 0 {
		t.Fatalf("cancelled run imported %d mailboxes", count)
	}
}
