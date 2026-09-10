package store

import (
	"fmt"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestMailAccountsPageFiltersSortsAndReturnsGlobalCounts(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 12; i++ {
		email := fmt.Sprintf("account-%02d@example.com", i)
		if _, err := s.SaveMailAccount(
			model.MailAccountProfile{Email: email, Label: fmt.Sprintf("Mailbox %02d", i), ManagementScope: "mail"},
			model.MailAccountCredentials{Email: email, PickupURL: "https://mail.example/" + email},
		); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}

	inside, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "account-00@example.com", UserID: "user-inside"}, "header.payload.signature")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateFreeAccount(inside.ID, func(item *model.FreeAccountProfile) {
		item.AcceptStatus = "completed"
	}); err != nil {
		t.Fatal(err)
	}
	removed, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "account-01@example.com", UserID: "user-removed"}, "header.payload.signature")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateFreeAccount(removed.ID, func(item *model.FreeAccountProfile) {
		item.AcceptStatus = "completed"
		item.RemoveStatus = "completed"
	}); err != nil {
		t.Fatal(err)
	}

	first, err := s.MailAccountsPage("mail", "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 12 || len(first.Items) != 10 {
		t.Fatalf("first page total/items = %d/%d, want 12/10", first.Total, len(first.Items))
	}
	if first.Items[0].Email != "account-11@example.com" {
		t.Fatalf("first account = %q, want newest outside account", first.Items[0].Email)
	}
	if first.Counts["all"] != 12 || first.SpaceCounts["outside"] != 10 || first.SpaceCounts["inside"] != 1 || first.SpaceCounts["removed"] != 1 {
		t.Fatalf("unexpected global counts: methods=%v spaces=%v", first.Counts, first.SpaceCounts)
	}
	second, err := s.MailAccountsPage("mail", "", "", 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if second.Total != 12 || len(second.Items) != 2 || second.Items[0].Email != "account-00@example.com" || second.Items[1].Email != "account-01@example.com" {
		t.Fatalf("unexpected second page: total=%d items=%+v", second.Total, second.Items)
	}
	searched, err := s.MailAccountsPage("mail", "mailbox 03", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if searched.Total != 1 || len(searched.Items) != 1 || searched.Items[0].Email != "account-03@example.com" {
		t.Fatalf("unexpected search result: total=%d items=%+v", searched.Total, searched.Items)
	}
	insideOnly, err := s.MailAccountsPage("mail", "", "inside", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if insideOnly.Total != 1 || len(insideOnly.Items) != 1 || insideOnly.Items[0].Email != "account-00@example.com" {
		t.Fatalf("unexpected inside filter: total=%d items=%+v", insideOnly.Total, insideOnly.Items)
	}
}

func TestFreeAccountsPageSummaryIsIndependentFromCurrentPage(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 12; i++ {
		profile, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{
			Email: fmt.Sprintf("free-%02d@example.com", i), UserID: fmt.Sprintf("free-user-%02d", i),
		}, "header.payload.signature")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
			switch i {
			case 0:
				item.AcceptStatus = "completed"
				item.SeatType = "prolite"
				item.Quota7D = &model.FreeQuotaWindow{UsedPercent: 25}
			case 1:
				item.AcceptStatus = "completed"
				item.RemoveStatus = "completed"
			case 2:
				item.AdminAccountID = "admin-1"
				item.InviteStatus = "running"
				item.SeatType = "prolite"
			case 3:
				item.OAuthStatus = "completed"
				item.PushStatus = "completed"
			}
		}); err != nil {
			t.Fatal(err)
		}
	}

	items, total, summary, err := s.FreeAccountsPage("", 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 12 || len(items) != 2 || summary.All != 12 || summary.Inside != 1 || summary.Removed != 1 || summary.Outside != 10 {
		t.Fatalf("unexpected page/summary: total=%d items=%d summary=%+v", total, len(items), summary)
	}
	if summary.InsidePremium != 1 || summary.Quota7DCount != 1 || summary.Quota7DRemainingTotal != 75 {
		t.Fatalf("unexpected quota summary: %+v", summary)
	}
	if got := summary.PendingSeatsByAdmin["admin-1"]["premium"]; got != 1 {
		t.Fatalf("pending premium seats = %d, want 1", got)
	}
}

func TestSMSPhonesUsesDatabasePaginationAndExactLookup(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 12; i++ {
		added, err := s.AddSMSPhone("generic", fmt.Sprintf("1550000%04d", i), "", fmt.Sprintf("https://sms.example/%d", i), "", "", 3)
		if err != nil || !added {
			t.Fatalf("add phone %d: added=%v err=%v", i, added, err)
		}
	}
	items, stats, total, err := s.SMSPhones("", "", 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 12 || len(items) != 2 || stats["total"] != 12 || stats["available"] != 12 {
		t.Fatalf("unexpected SMS page: total=%d len=%d stats=%v", total, len(items), stats)
	}
	id, _ := items[1]["id"].(string)
	item, err := s.SMSPhone(id)
	if err != nil {
		t.Fatal(err)
	}
	if item["id"] != id {
		t.Fatalf("exact lookup returned %#v, want %q", item["id"], id)
	}
}

func TestAutoRotationEventsPageFiltersBeforePagination(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	events := make([]model.AutoRotationEvent, 0, 12)
	for i := 0; i < 12; i++ {
		eventType := "step"
		if i%2 == 0 {
			eventType = "retry"
		}
		events = append(events, model.AutoRotationEvent{
			ID: fmt.Sprintf("event-%02d", i), RunID: "run-1", AccountID: fmt.Sprintf("account-%d", i%3),
			Email: fmt.Sprintf("user-%02d@example.com", i), Type: eventType, Message: fmt.Sprintf("message %02d", i),
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		})
	}
	if err := s.AddAutoRotationEvents(events); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.AutoRotationEventsPage("run-1", "", "", "retry", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 || len(items) != 6 || items[0].ID != "event-10" {
		t.Fatalf("unexpected event filter/order: total=%d items=%+v", total, items)
	}
	items, total, err = s.AutoRotationEventsPage("", "", "account-1", "", "user-10", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Email != "user-10@example.com" {
		t.Fatalf("unexpected account/query filter: total=%d items=%+v", total, items)
	}
}
