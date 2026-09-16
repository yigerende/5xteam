package httpapi

import (
	"context"
	"sort"
	"time"
)

type executionLogCoverage struct {
	SnapshotOnly     bool       `json:"snapshot_only"`
	EventCount       int        `json:"event_count"`
	Order            string     `json:"order"`
	OldestEventAt    *time.Time `json:"oldest_event_at,omitempty"`
	NewestEventAt    *time.Time `json:"newest_event_at,omitempty"`
	RetentionCutoff  time.Time  `json:"retention_cutoff"`
	FlushRequestedAt time.Time  `json:"flush_requested_at"`
	FlushCompleted   bool       `json:"flush_completed"`
	FlushError       string     `json:"flush_error,omitempty"`
	RunningOAuthJobs int        `json:"running_oauth_jobs"`
	HistoricalEvents int        `json:"events_without_export_v2_diagnostics"`
}

type executionLogHealth struct {
	Scope         string    `json:"scope"`
	ProcessSince  time.Time `json:"process_since"`
	DroppedEvents uint64    `json:"dropped_events"`
	WriteErrors   uint64    `json:"write_errors"`
	LastError     string    `json:"last_error,omitempty"`
}

func (s *Server) executionLogSnapshot(ctx context.Context, accountID, runID, taskID string) (executionLogExport, error) {
	payload := executionLogExport{SchemaVersion: 2, Retention: "48h"}
	payload.Coverage = executionLogCoverage{
		SnapshotOnly: true, Order: "newest_first", FlushRequestedAt: time.Now(),
	}
	flushErr := s.flushAuditEvents(ctx)
	payload.Coverage.FlushCompleted = flushErr == nil
	if flushErr != nil {
		payload.Coverage.FlushError = redactSensitiveText(flushErr.Error())
	}
	events, err := s.store.ExecutionLogEvents(accountID, runID, taskID)
	if err != nil {
		return payload, err
	}
	payload.Events = redactExecutionEvents(events)
	payload.Coverage.EventCount = len(events)
	if len(events) > 0 {
		payload.Coverage.NewestEventAt = &events[0].CreatedAt
		payload.Coverage.OldestEventAt = &events[len(events)-1].CreatedAt
	}
	for _, event := range events {
		if event.Details["diagnostics_version"] == nil {
			payload.Coverage.HistoricalEvents++
		}
	}
	payload.OAuthJobs = []map[string]any{}
	s.oauthMu.RLock()
	for _, job := range s.oauthJobs {
		if accountID != "" && job["account_id"] != accountID || runID != "" && job["run_id"] != runID || taskID != "" && job["task_id"] != taskID {
			continue
		}
		row := map[string]any{}
		for _, key := range []string{"job_id", "account_id", "cycle_id", "run_id", "task_id", "trigger", "source", "outer_attempt", "outer_max_attempts", "status", "error", "created_at", "started_at", "updated_at", "completed_at", "last_event_at", "last_stage", "round", "quality_attempt"} {
			if value, ok := job[key]; ok {
				row[key] = value
			}
		}
		payload.OAuthJobs = append(payload.OAuthJobs, redactMap(row))
		if job["status"] == "queued" || job["status"] == "running" {
			payload.Coverage.RunningOAuthJobs++
		}
	}
	s.oauthMu.RUnlock()
	sort.Slice(payload.OAuthJobs, func(i, j int) bool {
		a, _ := payload.OAuthJobs[i]["job_id"].(string)
		b, _ := payload.OAuthJobs[j]["job_id"].(string)
		return a < b
	})
	s.auditErrorMu.Lock()
	payload.AuditHealth = executionLogHealth{
		Scope: "all_accounts_in_current_process", ProcessSince: s.auditStartedAt,
		DroppedEvents: s.auditDropped.Load(), WriteErrors: s.auditWriteErrors.Load(), LastError: s.auditLastError,
	}
	s.auditErrorMu.Unlock()
	payload.ExportedAt = time.Now()
	payload.Coverage.RetentionCutoff = payload.ExportedAt.Add(-48 * time.Hour)
	return payload, nil
}
