package store

import (
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestAutoRotationSettingsAndRunPersistence(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.SaveAutoRotationSettings(model.AutoRotationSettings{Enabled: true, ThresholdPercent: 42, IntervalSeconds: 60, Concurrency: 4, MaxPerRun: 3, RetryCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxPerRun != 3 || !s.AutoRotationSettings().Enabled {
		t.Fatalf("settings not persisted: %+v", got)
	}
	run := model.AutoRotationRun{ID: "run-1", Status: "running", StartedAt: time.Now()}
	if err := s.SaveAutoRotationRun(run); err != nil {
		t.Fatal(err)
	}
	if len(s.AutoRotationRuns()) != 1 {
		t.Fatal("run not persisted")
	}
}

func TestAdminCapacitySnapshotPersistence(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fetched := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	want := model.AdminSeatCapacity{
		Premium:   model.AdminSeatBucket{Total: 9, Used: 8, Remaining: 1},
		Standard:  model.AdminSeatBucket{Total: 2, Used: 1, Remaining: 1},
		FetchedAt: fetched,
	}
	if err := s.SaveAdminCapacitySnapshot("admin-1", want); err != nil {
		t.Fatal(err)
	}
	got, ok := s.AdminCapacitySnapshot("admin-1")
	if !ok {
		t.Fatal("snapshot not found")
	}
	if got.Premium != want.Premium || got.Standard != want.Standard || !got.FetchedAt.Equal(want.FetchedAt) {
		t.Fatalf("snapshot mismatch: got=%+v want=%+v", got, want)
	}
	all := s.AdminCapacitySnapshots()
	if len(all) != 1 || all["admin-1"].Premium.Total != 9 {
		t.Fatalf("unexpected snapshot map: %+v", all)
	}
}

func TestAutoRotationClaimIsAtomic(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "a@example.com", UserID: "u1"}, "header.payload.sig")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); ok, _ := s.ClaimAutoRotationAccount(p.ID, "task"); results <- ok }(i)
	}
	wg.Wait()
	close(results)
	count := 0
	for ok := range results {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one claim, got %d", count)
	}
}

func TestSeatReservationsCountAndRelease(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.CreateSeatReservation("admin", "a1", "prolite")
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveSeatReservations("admin", "prolite") != 1 {
		t.Fatal("reservation not counted")
	}
	if err := s.ReleaseSeatReservation(a); err != nil {
		t.Fatal(err)
	}
	if s.ActiveSeatReservations("admin", "prolite") != 0 {
		t.Fatal("reservation not released")
	}
}

func TestSeatReservationSameAccountCannotDuplicate(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CreateSeatReservation("admin", "same-account", "prolite"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSeatReservation("admin", "same-account", "prolite"); err == nil {
		t.Fatal("expected unique account reservation conflict")
	}
}

func TestAutoRotationEventPersistenceAndFiltering(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AddAutoRotationEvent(model.AutoRotationEvent{ID: "e1", RunID: "r1", TaskID: "t1", Type: "seat_query", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAutoRotationEvent(model.AutoRotationEvent{ID: "e2", RunID: "r2", TaskID: "t2", Type: "request", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := s.AutoRotationEvents("r1", ""); len(got) != 1 || got[0].ID != "e1" {
		t.Fatalf("unexpected filtered events: %+v", got)
	}
}
