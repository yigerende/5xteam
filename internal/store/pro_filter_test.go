package store

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestProAccountFiltersPartitionProgressAndExport(t *testing.T) {
	s := transferStore(t)
	now := time.Now()
	tests := []struct {
		name, state string
		profile     model.MailAccountProfile
		credentials model.MailAccountCredentials
	}{
		{"new", "unmerged", model.MailAccountProfile{}, model.MailAccountCredentials{}},
		{"defaults", "unmerged", model.MailAccountProfile{InviteStatus: "pending", AcceptStatus: "not_started", ProAuto: model.ProAutoState{Steps: map[string]string{"login": "not_started", "merge": "pending"}}, ProManualStages: map[string]model.ProStageProgress{"login": {Status: "pending"}}}, model.MailAccountCredentials{}},
		{"credentials-only", "unmerged", model.MailAccountProfile{}, model.MailAccountCredentials{AccessToken: "at", RefreshToken: "rt"}},
		{"manual-login", "in_progress", model.MailAccountProfile{ProManualStages: map[string]model.ProStageProgress{"login": {Status: "running"}}}, model.MailAccountCredentials{}},
		{"manual-failed", "in_progress", model.MailAccountProfile{ProManualStages: map[string]model.ProStageProgress{"oauth": {Status: "failed"}}}, model.MailAccountCredentials{}},
		{"legacy-login", "in_progress", model.MailAccountProfile{}, model.MailAccountCredentials{AccessToken: "at", ChatGPTSession: `{}`}},
		{"legacy-oauth", "in_progress", model.MailAccountProfile{OAuthAuthorizedAt: &now}, model.MailAccountCredentials{AccessToken: "at", RefreshToken: "rt"}},
		{"legacy-push", "in_progress", model.MailAccountProfile{PushStatus: "completed"}, model.MailAccountCredentials{}},
		{"legacy-quota", "in_progress", model.MailAccountProfile{QuotaStatus: "failed"}, model.MailAccountCredentials{}},
		{"merge-running", "in_progress", model.MailAccountProfile{ProWorkflowRunning: true}, model.MailAccountCredentials{}},
		{"merge-step", "in_progress", model.MailAccountProfile{InviteStatus: "completed", AcceptStatus: "failed"}, model.MailAccountCredentials{}},
		{"merge-unknown", "in_progress", model.MailAccountProfile{TransferStatus: "unknown"}, model.MailAccountCredentials{}},
		{"paid-legacy", "in_progress", model.MailAccountProfile{}, model.MailAccountCredentials{}},
		{"merged", "merged", model.MailAccountProfile{SpaceMergedOnce: true}, model.MailAccountCredentials{}},
		{"merged-removal-failed", "merged", model.MailAccountProfile{SpaceMergedOnce: true, ProAuto: model.ProAutoState{ID: "done", Status: "failed"}, RemoveStatus: "failed"}, model.MailAccountCredentials{}},
	}
	for _, status := range []string{"running", "waiting_quota", "awaiting_push", "failed", "interrupted", "stopped", "completed"} {
		tests = append(tests, struct {
			name, state string
			profile     model.MailAccountProfile
			credentials model.MailAccountCredentials
		}{"auto-" + status, "in_progress", model.MailAccountProfile{ProAuto: model.ProAutoState{ID: status, Status: status}}, model.MailAccountCredentials{}})
	}
	want := map[string][]string{"unmerged": {}, "in_progress": {}, "merged": {}}
	for i, tc := range tests {
		email := tc.name + "@example.com"
		tc.profile.Email, tc.profile.ManagementScope, tc.credentials.Email = email, "pro", email
		managed := now.Add(time.Duration(i) * time.Minute)
		tc.profile.ProManagedAt = &managed
		if _, err := s.SaveMailAccount(tc.profile, tc.credentials); err != nil {
			t.Fatal(err)
		}
		want[tc.state] = append(want[tc.state], email)
	}
	if _, err := s.SaveMailAccount(model.MailAccountProfile{Email: "mail@example.com", ManagementScope: "mail"}, model.MailAccountCredentials{}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGPTPayOrder(gptpay.Order{ID: "old", Email: "paid-legacy@example.com", Status: "success", CreatedAt: now}, gptpay.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	for state, expected := range want {
		t.Run(state, func(t *testing.T) {
			items, total, summary, err := s.ProAccountsPage("", state, 100, 0)
			if err != nil || total != len(expected) || summary.All != len(tests) || summary.Unmerged != len(want["unmerged"]) || summary.InProgress != len(want["in_progress"]) || summary.Merged != len(want["merged"]) {
				t.Fatalf("bad total/summary: %d %+v %v", total, summary, err)
			}
			var listed []string
			for _, item := range items {
				listed = append(listed, item.Email)
			}
			sort.Strings(listed)
			sort.Strings(expected)
			exported, err := s.ProExportEmails("", "  "+state+"  ")
			if err != nil || !reflect.DeepEqual(listed, expected) || !reflect.DeepEqual(exported, expected) {
				t.Fatalf("list/export mismatch: listed=%v exported=%v want=%v err=%v", listed, exported, expected, err)
			}
			for i := 0; i < len(items); i += 10 {
				page, total, _, err := s.ProAccountsPage("", state, 10, i)
				if err != nil || total != len(expected) || len(page) != min(10, len(items)-i) {
					t.Fatal("pagination count differs from full filter", total, len(page), err)
				}
				for j, item := range page {
					if item.Email != items[i+j].Email {
						t.Fatal("pagination order differs from full filter")
					}
				}
			}
			for _, email := range expected {
				found, total, _, err := s.ProAccountsPage(email, state, 10, 0)
				exported, exportErr := s.ProExportEmails(email, state)
				if err != nil || exportErr != nil || total != 1 || len(found) != 1 || len(exported) != 1 || exported[0] != email {
					t.Fatal("search/filter combination", email, err, exportErr)
				}
			}
		})
	}
	// Changing progress moves the same account instead of duplicating it.
	for _, stage := range []string{"running", "failed", "completed"} {
		_, err := s.UpdateProAccount("new@example.com", func(p *model.MailAccountProfile) {
			p.ProManualStages = map[string]model.ProStageProgress{"login": {Status: stage}}
		})
		if err != nil {
			t.Fatal(err)
		}
		items, total, _, err := s.ProAccountsPage("new@", "in_progress", 10, 0)
		if err != nil || total != 1 || len(items) != 1 {
			t.Fatal("manual transition", stage, err)
		}
	}
}
