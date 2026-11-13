package store

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func markerStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	email := "marker@example.com"
	_, e = s.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{})
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.UpdateMailAccountManagementScope(email, "pro")
	if e != nil {
		t.Fatal(e)
	}
	return s, email
}
func seedMarker(t *testing.T, s *Store, email string) {
	t.Helper()
	_, e := s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.PushProvider = "sub2"
		p.Sub2AccountID = 7
		p.ProAuto = model.ProAutoState{ID: "flow", Stage: "merge", Steps: map[string]string{"merge": "completed"}}
	})
	if e != nil {
		t.Fatal(e)
	}
}
func claimMarker(t *testing.T, s *Store) SIWCMarkerTask {
	t.Helper()
	v, e := s.ClaimSIWCMarkers(time.Now().Add(time.Second))
	if e != nil || len(v) != 1 {
		t.Fatalf("claim: %d %v", len(v), e)
	}
	return v[0]
}
func TestSIWCMarkerDurableQueueAndLateAcknowledgement(t *testing.T) {
	s, email := markerStore(t)
	seedMarker(t, s, email)
	old := claimMarker(t, s)
	if len(old.SourceID) != 32 || old.Revision != 1 {
		t.Fatalf("invalid identity: %+v", old)
	}
	// A restart replaces the queued completion, even while its HTTP call is in flight.
	_, e := s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.ID = "second"; p.ProAuto.Steps["merge"] = "pending" })
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishSIWCMarker(old, "synced", "", time.Time{}); e != nil {
		t.Fatal(e)
	}
	current := claimMarker(t, s)
	if current.Revision <= old.Revision || current.Profile.SIWCWorkflowMarker(current.SourceID, current.Revision, current.ChangedAt, "merge").Completed {
		t.Fatal("late acknowledgement erased reset")
	}
	// Change back before that reset's response arrives: newest completion must be delivered.
	_, e = s.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProAuto.Steps["merge"] = "completed" })
	if e != nil {
		t.Fatal(e)
	}
	if e = s.FinishSIWCMarker(current, "retry", "old timeout", time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	newest := claimMarker(t, s)
	if newest.Revision <= current.Revision || newest.Profile.ProAuto.Steps["merge"] != "completed" {
		t.Fatal("new completion lost")
	}
	if e = s.FinishSIWCMarker(newest, "synced", "", time.Time{}); e != nil {
		t.Fatal(e)
	}
	states, e := s.SIWCMarkerStates([]string{email})
	if e != nil || states[email].State != "synced" {
		t.Fatal(states, e)
	}
	p, _, e := s.MailAccountCredential(email)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SetProStageOverride(email, "merge", "failed", p.UpdatedAt); e != nil {
		t.Fatal(e)
	}
	task := claimMarker(t, s)
	if task.Profile.SIWCWorkflowMarker(task.SourceID, task.Revision, task.ChangedAt, "merge").Completed {
		t.Fatal("manual override not captured")
	}
}

func TestSIWCMarkerAtomicRollbackAndNoQuotaEnqueue(t *testing.T) {
	s, email := markerStore(t)
	seedMarker(t, s, email)
	task := claimMarker(t, s)
	if e := s.FinishSIWCMarker(task, "synced", "", time.Time{}); e != nil {
		t.Fatal(e)
	}
	tx, e := s.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(`UPDATE mail_accounts SET profile=json_set(profile,'$.pro_auto.steps.merge','failed') WHERE email=?`, email)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 100; i++ {
		_, e = s.UpdateProAccount(email, func(p *model.MailAccountProfile) {
			now := time.Now()
			p.QuotaCheckedAt = &now
			p.ProAuto.UpdatedAt = now
			p.CostDownstreamSnapshot = float64(i)
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	tasks, e := s.ClaimSIWCMarkers(time.Now().Add(time.Hour))
	if e != nil || len(tasks) != 0 {
		t.Fatalf("quota poll enqueued work: %d %v", len(tasks), e)
	}
	states, e := s.SIWCMarkerStates([]string{email})
	if e != nil || states[email].Revision != task.Revision {
		t.Fatal("revision changed without completion changes", e)
	}
}

func TestSIWCMarkerReopenDeleteAndConcurrentClaim(t *testing.T) {
	s, email := markerStore(t)
	seedMarker(t, s, email)
	first := claimMarker(t, s)
	if e := s.FinishSIWCMarker(first, "retry", "timeout", time.Now()); e != nil {
		t.Fatal(e)
	}
	// Completion delivery remains possible after local account removal.
	if e := s.DeleteProAccount(email); e != nil {
		t.Fatal(e)
	}
	dir := s.path[:len(s.path)-len("state.db")]
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	var wg sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tasks, err := reopened.ClaimSIWCMarkers(time.Now().Add(time.Second))
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			count += len(tasks)
			for _, task := range tasks {
				if task.SourceID != first.SourceID || task.Revision != first.Revision {
					t.Error("identity changed on restart")
				}
				raw, _ := json.Marshal(task)
				if string(raw) == "" {
					t.Error("empty task")
				}
			}
		}()
	}
	wg.Wait()
	if count != 1 {
		t.Fatalf("claimed %d times", count)
	}
}
