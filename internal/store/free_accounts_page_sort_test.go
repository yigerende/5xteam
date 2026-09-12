package store

import (
	"fmt"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestFreeAccountsPageSortsBySpaceStateTimeBeforePagination(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	base := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	states := []string{"inside", "outside", "removed"}
	wantByState := make(map[string][]string)
	var wantAll []string
	for _, state := range states {
		for i := 0; i < 12; i++ {
			email := fmt.Sprintf("%s-%02d@example.com", state, i)
			profile, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: email}, "header.payload.signature")
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
				enteredAt := base.Add(-time.Duration(i) * time.Hour)
				stageAt := base.Add(time.Duration(i) * time.Hour)
				// Alternate offsets so lexical timestamp sorting gives the wrong order.
				if i%2 == 0 {
					stageAt = stageAt.In(time.FixedZone("UTC+8", 8*60*60))
				}
				item.ImportedAt = enteredAt
				item.CreatedAt = base.Add(time.Duration(i) * time.Minute)
				switch state {
				case "inside":
					item.AcceptStatus = "completed"
					item.JoinedAt = &stageAt
					item.RemovedAt = &enteredAt
				case "removed":
					item.AcceptStatus, item.RemoveStatus = "completed", "completed"
					item.JoinedAt, item.RemovedAt = &enteredAt, &stageAt
				case "outside":
					item.ImportedAt = stageAt
					item.JoinedAt, item.RemovedAt = &enteredAt, &enteredAt
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			wantByState[state] = append([]string{email}, wantByState[state]...)
		}
		wantAll = append(wantAll, wantByState[state]...)
	}
	wantByState[""] = wantAll
	for _, state := range append(states, "") {
		t.Run("filter_"+state, func(t *testing.T) {
			want := wantByState[state]
			for offset := 0; offset < len(want); offset += 10 {
				items, total, summary, err := s.FreeAccountsPage(state, 10, offset)
				if err != nil {
					t.Fatal(err)
				}
				end := min(offset+10, len(want))
				if total != len(want) || len(items) != end-offset {
					t.Fatalf("offset %d: total/items = %d/%d, want %d/%d", offset, total, len(items), len(want), end-offset)
				}
				for i, item := range items {
					if item.Email != want[offset+i] {
						t.Errorf("position %d: email = %q, want %q", offset+i, item.Email, want[offset+i])
					}
				}
				if summary.All != 36 || summary.Inside != 12 || summary.Outside != 12 || summary.Removed != 12 {
					t.Fatalf("unexpected summary: %+v", summary)
				}
			}
		})
	}
}

func TestFreeAccountsPageSortTimeFallbacksAndTies(t *testing.T) {
	for _, state := range []string{"inside", "outside", "removed"} {
		t.Run(state, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			base := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
			zero := time.Time{}
			later := base.Add(time.Hour)
			latest := base.Add(2 * time.Hour)
			var ids []string
			for i, fixture := range []struct {
				name       string
				importedAt time.Time
				createdAt  time.Time
				stageAt    *time.Time
			}{
				{"legacy_missing_stage", base, base, nil},
				{"legacy_zero_stage", later, base, &zero},
				{"legacy_zero_import", zero, latest, nil},
				{"tie_a", latest.Add(time.Hour), base, nil},
				{"tie_b", latest.Add(time.Hour), base, nil},
			} {
				email := fixture.name + "@example.com"
				profile, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: email}, "header.payload.signature")
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, profile.ID)
				_, err = s.UpdateFreeAccount(profile.ID, func(item *model.FreeAccountProfile) {
					item.ImportedAt, item.CreatedAt = fixture.importedAt, fixture.createdAt
					stageAt := fixture.stageAt
					if i >= 3 && state != "outside" {
						// Equal stage timestamps must not be ordered by list entry time.
						stageAt = &fixture.importedAt
						item.ImportedAt = base.Add(-time.Duration(i) * time.Hour)
					}
					if state == "inside" || state == "removed" {
						item.AcceptStatus = "completed"
						item.JoinedAt = stageAt
					}
					if state == "removed" {
						item.RemoveStatus = "completed"
						item.RemovedAt = stageAt
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			want := []string{ids[3], ids[4], ids[2], ids[1], ids[0]}
			if want[0] < want[1] {
				want[0], want[1] = want[1], want[0]
			}
			items, total, _, err := s.FreeAccountsPage(state, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			if total != len(want) || len(items) != len(want) {
				t.Fatalf("total/items = %d/%d, want %d/%d", total, len(items), len(want), len(want))
			}
			for i, item := range items {
				if item.ID != want[i] {
					t.Errorf("position %d: id = %q (%s), want %q", i, item.ID, item.Email, want[i])
				}
			}
		})
	}
}
