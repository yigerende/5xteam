package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestAccountEventCursorHandlesEqualTimesAndNewEvents(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	events := make([]model.AutoRotationEvent, 123)
	for i := range events {
		events[i] = model.AutoRotationEvent{ID: fmt.Sprintf("event-%03d", i), AccountID: "account", CreatedAt: now}
	}
	if err := s.AddAutoRotationEvents(events); err != nil {
		t.Fatal(err)
	}
	page, err := s.AccountEventsPage("account", "", 50)
	if err != nil || len(page.Events) != 50 || !page.HasMore {
		t.Fatalf("first page: count=%d err=%v", len(page.Events), err)
	}
	if _, err := s.AccountEventsPage("other", page.NextCursor, 50); !errors.Is(err, ErrInvalidEventCursor) {
		t.Fatalf("cursor from another account accepted: %v", err)
	}
	if err := s.AddAutoRotationEvent(model.AutoRotationEvent{ID: "new-event", AccountID: "account", CreatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	// Cursor pagination must survive retention deleting its boundary event.
	if _, err := s.db.Exec("DELETE FROM auto_rotation_events WHERE id=?", page.Events[len(page.Events)-1].ID); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for {
		for _, event := range page.Events {
			if seen[event.ID] || event.ID == "new-event" || event.AccountID != "account" {
				t.Fatalf("duplicate or unexpected event: %s", event.ID)
			}
			seen[event.ID] = true
		}
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatal("last page retained a cursor")
			}
			break
		}
		page, err = s.AccountEventsPage("account", page.NextCursor, 50)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != len(events) {
		t.Fatalf("pagination skipped events: %d/%d", len(seen), len(events))
	}
	if empty, err := s.AccountEventsPage("other", "", 50); err != nil || len(empty.Events) != 0 || empty.HasMore {
		t.Fatalf("unexpected empty account page: %+v err=%v", empty, err)
	}
}
