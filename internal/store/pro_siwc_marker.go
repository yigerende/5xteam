package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

type SIWCMarkerTask struct {
	Email     string                   `json:"email"`
	Revision  int64                    `json:"revision"`
	State     string                   `json:"state"`
	Error     string                   `json:"error,omitempty"`
	Attempts  int                      `json:"attempts"`
	ChangedAt time.Time                `json:"changed_at"`
	SyncedAt  *time.Time               `json:"synced_at,omitempty"`
	SourceID  string                   `json:"-"`
	Profile   model.MailAccountProfile `json:"-"`
}

// Atomic with every profile writer, including manual overrides and restart
// reconciliation. Quota timestamps/cost polling do not enqueue marker updates.
func (s *Store) initializeSIWCMarkers() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS pro_siwc_marker_source(id INTEGER PRIMARY KEY CHECK(id=1),source_id TEXT NOT NULL,seq INTEGER NOT NULL);
 INSERT OR IGNORE INTO pro_siwc_marker_source VALUES(1,lower(hex(randomblob(16))),0);
 CREATE TABLE IF NOT EXISTS pro_siwc_markers(email TEXT PRIMARY KEY,revision INTEGER NOT NULL,profile TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',error TEXT NOT NULL DEFAULT '',attempts INTEGER NOT NULL DEFAULT 0,changed_at TEXT NOT NULL,synced_at TEXT,next_at INTEGER NOT NULL DEFAULT 0,claim_until INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX IF NOT EXISTS pro_siwc_markers_due ON pro_siwc_markers(next_at) WHERE next_at>0;`)
	if err != nil {
		return err
	}
	paths := []string{"$.space_merged_once", "$.pro_auto.started_at", "$.pro_deactivation.updated_at", "$.pro_deactivation.confirmation_source", "$.pro_merchant.request_id", "$.sub2_account_id", "$.push_provider", "$.pro_push_downstream", "$.pro_restock_target_id", "$.pro_siwc.account_id", "$.pro_siwc.target_id", "$.pro_auto.id", "$.pro_auto.order_id", "$.pro_auto.stage", "$.pro_auto.steps.merge", "$.pro_auto.steps.merge_v2", "$.pro_auto.steps.deactivate", "$.pro_auto.steps.merchant", "$.pro_merchant.status", "$.pro_merchant.completed_at", "$.pro_merchant.error", "$.pro_deactivation.status", "$.pro_deactivation.submitted_at", "$.pro_deactivation.confirmed_at", "$.pro_deactivation.error", "$.pro_merge_v2.status", "$.pro_merge_v2_restart.id", "$.pro_manual_stages.merge", "$.pro_manual_stages.merge_v2", "$.pro_manual_stages.deactivate", "$.pro_manual_stages.merchant", "$.pro_stage_overrides.merge", "$.pro_stage_overrides.deactivate", "$.pro_stage_overrides.merchant", "$.pro_stage_overrides.merge_v2", "$.pro_invite_status", "$.pro_accept_status", "$.pro_transfer_status", "$.pro_remove_status", "$.pro_workflow_running", "$.pro_post_merge_groups.status"}
	checks := make([]string, 0, len(paths))
	for _, p := range paths {
		checks = append(checks, fmt.Sprintf("json_extract(NEW.profile,'%s') IS NOT json_extract(OLD.profile,'%s')", p, p))
	}
	gate := `json_extract(NEW.profile,'$.management_scope')='pro' AND json_extract(NEW.profile,'$.sub2_account_id')>0 AND json_extract(NEW.profile,'$.push_provider')='sub2'`
	body := `BEGIN
 UPDATE pro_siwc_marker_source SET seq=seq+1 WHERE id=1;
 INSERT INTO pro_siwc_markers(email,revision,profile,state,changed_at,next_at)
 VALUES(NEW.email,(SELECT seq FROM pro_siwc_marker_source WHERE id=1),NEW.profile,'pending',strftime('%Y-%m-%dT%H:%M:%fZ','now'),CAST(strftime('%s','now') AS INTEGER)*1000)
 ON CONFLICT(email) DO UPDATE SET revision=excluded.revision,profile=excluded.profile,state='pending',error='',attempts=0,changed_at=excluded.changed_at,next_at=excluded.next_at,claim_until=0;
 END;`
	_, err = s.db.Exec(`CREATE TRIGGER IF NOT EXISTS pro_siwc_marker_insert AFTER INSERT ON mail_accounts WHEN ` + gate + ` ` + body + `
 CREATE TRIGGER IF NOT EXISTS pro_siwc_marker_update AFTER UPDATE OF profile ON mail_accounts WHEN ` + gate + ` AND (` + strings.Join(checks, " OR ") + `) ` + body)
	return err
}

func (s *Store) ClaimSIWCMarkers(now time.Time) ([]SIWCMarkerTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT email,revision,profile,attempts,changed_at,(SELECT source_id FROM pro_siwc_marker_source WHERE id=1) FROM pro_siwc_markers WHERE next_at>0 AND next_at<=? AND claim_until<=? ORDER BY next_at LIMIT 2`, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	tasks := []SIWCMarkerTask{}
	for rows.Next() {
		var t SIWCMarkerTask
		var raw, at string
		if err = rows.Scan(&t.Email, &t.Revision, &raw, &t.Attempts, &at, &t.SourceID); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &t.Profile); err != nil {
			break
		}
		if t.ChangedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			break
		}
		tasks = append(tasks, t)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if _, err = tx.Exec(`UPDATE pro_siwc_markers SET claim_until=? WHERE email=? AND revision=?`, now.Add(45*time.Second).UnixMilli(), t.Email, t.Revision); err != nil {
			return nil, err
		}
	}
	return tasks, tx.Commit()
}

func (s *Store) FinishSIWCMarker(t SIWCMarkerTask, state, message string, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	due := int64(0)
	if !next.IsZero() {
		due = next.UnixMilli()
	}
	var synced any
	if state == "synced" {
		synced = formatTime(time.Now())
	}
	_, err := s.db.Exec(`UPDATE pro_siwc_markers SET state=?,error=?,attempts=attempts+1,synced_at=COALESCE(?,synced_at),next_at=?,claim_until=0 WHERE email=? AND revision=?`, state, message, synced, due, t.Email, t.Revision)
	return err
}

func (s *Store) SIWCMarkerStates(emails []string) (map[string]SIWCMarkerTask, error) {
	out := map[string]SIWCMarkerTask{}
	if len(emails) == 0 {
		return out, nil
	}
	if len(emails) > 500 {
		return nil, fmt.Errorf("too many marker accounts")
	}
	args := make([]any, len(emails))
	marks := make([]string, len(emails))
	for i, e := range emails {
		args[i] = strings.ToLower(e)
		marks[i] = "?"
	}
	rows, err := s.db.Query(`SELECT email,revision,state,error,attempts,changed_at,synced_at FROM pro_siwc_markers WHERE email IN (`+strings.Join(marks, ",")+`) AND state<>'skipped'`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t SIWCMarkerTask
		var at string
		var synced *string
		if err = rows.Scan(&t.Email, &t.Revision, &t.State, &t.Error, &t.Attempts, &at, &synced); err != nil {
			return nil, err
		}
		t.ChangedAt, _ = time.Parse(time.RFC3339Nano, at)
		if synced != nil {
			value, _ := time.Parse(time.RFC3339Nano, *synced)
			t.SyncedAt = &value
		}
		out[t.Email] = t
	}
	return out, rows.Err()
}

func (s *Store) RetrySIWCMarker(email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE pro_siwc_markers SET next_at=?,state='pending',error='' WHERE email=? AND state IN ('retry','pending')`, time.Now().UnixMilli(), strings.ToLower(strings.TrimSpace(email)))
	return err
}
