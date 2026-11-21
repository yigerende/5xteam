package httpapi

import (
	"context"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
)

// One serial preparation worker handles already-reserved tasks. It shares the
// existing stop/shutdown lifecycle but never holds the monitoring tick lock.
// Issuance still uses the original admission and idempotency checks.
func (s *Server) queueRestockPreparation(parent context.Context, c restock.Config, p model.ProSettings, snap restock.Snapshot, imports, dispatch bool) {
	s.proMergeMu.Lock()
	running := s.proAutoRunning
	s.proMergeMu.Unlock()
	s.proAutoMu.Lock()
	busy := s.proAutoClosed || s.proRestockStartJobs["restock-preparation-worker"] != nil || max(running, len(s.proAutoJobs)) >= p.Concurrency
	s.proAutoMu.Unlock()
	if busy {
		return
	}
	// Do not create idle workers: they must not defer reconciliation of an
	// account that has already completed its preparation and push.
	tasks, err := s.store.ProRestockActiveTasks()
	if err != nil {
		return
	}
	pending := false
	for _, task := range tasks {
		if dispatch && task.Status == "reserved" && task.Version == c.Version && (task.NextCardCheckAt == nil || !task.NextCardCheckAt.After(time.Now())) {
			pending = true
			break
		}
		if imports && task.Active() && task.Status != "reserved" && task.CardProvider == "nexovcc" && (task.NextCardCheckAt == nil || !task.NextCardCheckAt.After(time.Now())) {
			profile, _, e := s.store.MailAccountCredential(task.Email)
			if e == nil && profile.ProAuto.ID == "" && !profile.ProAuto.StopRequested && profile.PushStatus != "completed" {
				op, e := s.store.NexovccRestockOperation(task.ID)
				if e == nil && op.Serial != "" && (op.Status == "import_pending" || op.Status == "succeeded") {
					pending = true
					break
				}
			}
		}
	}
	if !pending {
		return
	}
	ctx, finish, err := s.beginRestockStart(parent, "restock-preparation-worker")
	if err != nil {
		return
	}
	go func() {
		defer finish()
		current, err := s.store.ProRestockConfig()
		if err != nil || current.Version != c.Version || !current.Enabled || current.Mode != "auto" || ctx.Err() != nil {
			return
		}
		if imports {
			s.continueProRestockCardImports(ctx, c, p)
		}
		if dispatch && ctx.Err() == nil {
			s.dispatchProRestock(ctx, c, p, snap)
		}
	}()
}
