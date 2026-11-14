package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"context"
	"net/http"
	"testing"
	"time"
)

func TestRestockWatchdogCancelsOnlyItsWorkersWithoutMonitor(t *testing.T) {
	s, st, email := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected remote call") })
	s.proAutoCancel()
	s.proAutoWG.Wait()
	c := restock.DefaultConfig()
	c.Enabled, c.Mode, c.GroupID = true, "auto", 1
	c, _ = st.SaveProRestockConfig(c)
	now := time.Now()
	tasks, err := st.ReserveProRestock(restock.Decision{ID: "watchdog-http", At: now, Version: c.Version, Suggested: 1}, "", 0)
	if err != nil || len(tasks) != 1 {
		t.Fatal(err)
	}
	st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "flow", Stage: "login", Status: "running", StartedAt: now}
	})
	job, cancel := context.WithCancel(context.Background())
	defer cancel()
	other, otherCancel := context.WithCancel(context.Background())
	defer otherCancel()
	s.proAutoJobs = map[string]context.CancelFunc{email: cancel, "other@example.com": otherCancel}
	s.proRestockStartJobs = map[string]context.CancelFunc{tasks[0].ID: cancel, "restock-preparation-worker": otherCancel}
	s.proMergeJobs = map[string]context.CancelFunc{email: cancel}
	// A blocked/slow monitor must not prevent this independent local check.
	s.proRestock.tick.Lock()
	s.tickProRestockWatchdog(now.Add(11 * time.Minute))
	s.proRestock.tick.Unlock()
	if job.Err() == nil || other.Err() != nil {
		t.Fatal("wrong cancellation")
	}
	s.proAutoJobs[email] = otherCancel
	s.proMergeJobs[email] = otherCancel
	s.tickProRestockWatchdog(now.Add(12 * time.Minute))
	if other.Err() != nil {
		t.Fatal("repeated sweep stopped newer work")
	}
	s.proAutoJobs = nil
	s.proMergeJobs = nil
	s.proRestockStartJobs = nil
}
