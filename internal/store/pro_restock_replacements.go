package store

import (
	"chatgpt-space-merge/internal/restock"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func replacementTasks(tx *sql.Tx, snap restock.Snapshot) ([]restock.Task, error) {
	ids := make([]int64, 0, len(snap.Accounts))
	for _, a := range snap.Accounts {
		ids = append(ids, a.ID)
	}
	// Historical ready tasks matter only if their account is still in this
	// snapshot. Explicit coverage links and unresolved tasks remain unbounded.
	rows, err := tx.Query(`SELECT t.payload,coalesce(r.source,'') FROM pro_restock_tasks t LEFT JOIN pro_restock_replacements r ON r.task_id=t.id WHERE t.id IN (
 SELECT id FROM pro_restock_tasks WHERE status NOT IN ('ready','failed','cancelled')
 UNION SELECT task_id FROM pro_restock_replacements
 UNION SELECT id FROM pro_restock_tasks WHERE status='ready' AND json_extract(payload,'$.account_id') IN (SELECT value FROM json_each(?)) AND json_extract(payload,'$.binding')=? AND json_extract(payload,'$.group_id')=?
 ) ORDER BY t.created_at,t.id`, mustJSON(ids), snap.Binding, snap.GroupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []restock.Task{}
	for rows.Next() {
		var raw, source string
		var t restock.Task
		if err = rows.Scan(&raw, &source); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &t); err != nil {
			return nil, err
		}
		// Older payloads may retain a released source; that is assignment
		// history, not current coverage. Preserve it before applying the join.
		t.ForceMergeAssigned = t.ForceMergeAssigned || t.ForceMergeSource != "" || source != ""
		t.ForceMergeSource = source
		out = append(out, t)
	}
	return out, rows.Err()
}

// Reconcile links while holding the same SQLite write lock used by reservation.
// Confirmed failures release coverage; unknown payments retain it without ETA.
func (s *Store) ReconcileForceMerge(c restock.Config, snap restock.Snapshot, tasks []restock.Task) ([]restock.Task, error) {
	if !snap.Valid || !snap.LoadValid || snap.At.IsZero() || time.Since(snap.At) > time.Duration(c.FreshSeconds)*time.Second || snap.At.After(time.Now().Add(5*time.Second)) {
		return tasks, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE pro_restock_config SET payload=payload WHERE id=1`); err != nil {
		return nil, err
	}
	currentConfig, err := readRestockConfig(tx)
	if err != nil {
		return nil, err
	}
	if currentConfig.Version != c.Version {
		return tasks, nil
	}
	persisted, err := replacementTasks(tx, snap)
	if err != nil {
		return nil, err
	}
	sources := map[string]bool{}
	ready := map[int64]bool{}
	for _, a := range snap.Accounts {
		sources[a.ForceMergeKey] = true
		ready[a.ID] = a.Ready
	}

	for i, t := range persisted {
		if t.ForceMergeSource == "" {
			continue
		}

		if t.GroupID != snap.GroupID || (t.Binding != snap.Binding && !sources[t.ForceMergeSource]) {
			continue
		}
		if t.Status == "failed" || t.Status == "cancelled" || (t.Status == "ready" && (!sources[t.ForceMergeSource] || !ready[t.AccountID])) {
			if _, err = tx.Exec("DELETE FROM pro_restock_replacements WHERE source=?", t.ForceMergeSource); err != nil {
				return nil, err
			}
			t.ForceMergeSource = ""
			if _, err = tx.Exec("UPDATE pro_restock_tasks SET payload=? WHERE id=?", mustJSON(t), t.ID); err != nil {
				return nil, err
			}
		}
		persisted[i] = t
	}
	stats := restock.ForceMergeSummary(c, snap, persisted, snap.At)
	if c.Enabled && c.Mode == "auto" && stats.Triggered {
		// A time/concurrency/exit purchase may become ready before the average
		// deadline crosses its threshold. Reuse it only with original decision
		// evidence and only once, rather than ordering the same capacity again.
		if err = linkReadyForceMergeReplacements(tx, c, snap, persisted, stats.Sources); err != nil {
			return nil, err
		}
		stats = restock.ForceMergeSummary(c, snap, persisted, snap.At)
		// Adopt verified same-target automatic work so its eventual readiness remains
		// linked after restart. It consumes inflight, never this policy's purchase budget.
		available := 0
		known := map[string]bool{}
		for _, t := range persisted {
			known[t.ID] = true
			known[t.Email] = true
			if t.Active() && t.ForceMergeSource == "" && t.Binding == snap.Binding && t.GroupID == snap.GroupID {
				available++
			}
		}
		for _, t := range tasks {
			if available >= len(stats.Sources) {
				break
			}
			if known[t.ID] || known[t.Email] || !t.Active() || t.Binding != snap.Binding || t.GroupID != snap.GroupID || t.DecisionID != "" {
				continue
			}
			t.ID = "adopted:" + t.ID
			t.DecisionID = t.ID
			t.ManagedExternally = true
			t.Cost = 0
			if t.CreatedAt.IsZero() {
				t.CreatedAt = time.Now()
			}
			if _, err = tx.Exec("INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)", t.ID, t.Email, t.Status, formatTime(t.CreatedAt), 0, mustJSON(t)); err != nil {
				return nil, err
			}
			persisted = append(persisted, t)
			known[t.Email] = true
			available++
		}
		next := 0
		for i, t := range persisted {
			if next >= len(stats.Sources) {
				break
			}
			if !t.Active() || t.ForceMergeSource != "" || t.Binding != snap.Binding || t.GroupID != snap.GroupID {
				continue
			}
			if t.Status == "reserved" && (t.Version != c.Version || t.Plan != snap.NewPlan) {
				continue
			}
			if err = linkForceMergeReplacement(tx, &t, stats.Sources[next]); err != nil {
				return nil, err
			}
			persisted[i] = t
			next++
		}
	}
	links := map[string]restock.Task{}
	for _, t := range persisted {
		links[t.ID] = t
	}
	adopted := map[string]restock.Task{}
	for _, t := range persisted {
		if t.ManagedExternally {
			adopted[t.Email] = t
		}
	}
	returned := map[string]bool{}
	for i := range tasks {
		if t, ok := adopted[tasks[i].Email]; ok && tasks[i].DecisionID == "" {
			tasks[i] = t
		}
		tasks[i].ForceMergeSource = links[tasks[i].ID].ForceMergeSource
		tasks[i].ForceMergeAssigned = links[tasks[i].ID].ForceMergeAssigned
		returned[tasks[i].ID] = true
	}
	// A newly linked ready task can be older than the normal history window.
	// Include it in this very evaluation, not only in the next database read.
	for _, t := range persisted {
		if !returned[t.ID] && t.ForceMergeSource != "" && t.Binding == snap.Binding && t.GroupID == snap.GroupID {
			tasks = append(tasks, t)
		}
	}
	return tasks, tx.Commit()
}

func linkForceMergeReplacement(tx *sql.Tx, t *restock.Task, source string) error {
	t.ForceMergeSource, t.ForceMergeAssigned = source, true
	if _, err := tx.Exec("INSERT INTO pro_restock_replacements(source,task_id) VALUES(?,?)", source, t.ID); err != nil {
		return err
	}
	_, err := tx.Exec("UPDATE pro_restock_tasks SET payload=? WHERE id=?", mustJSON(*t), t.ID)
	return err
}

func linkReadyForceMergeReplacements(tx *sql.Tx, c restock.Config, snap restock.Snapshot, tasks []restock.Task, sources []string) error {
	if len(sources) == 0 {
		return nil
	}
	ready := map[int64]bool{}
	for _, a := range snap.Accounts {
		ready[a.ID] = a.Ready && !a.SupplyExcluded && !a.Leaving && a.Capacity > 0 && (a.ExpiresAt == nil || a.ExpiresAt.After(snap.At.Add(time.Duration(c.ForceMergeMinutes)*time.Minute)))
	}
	forecastConfig := c
	forecastConfig.ExitMinutes = max(c.ExitMinutes, c.ForceMergeMinutes)
	for _, row := range restock.ForecastExits(forecastConfig, snap, nil, 1, 0, snap.At).Rows {
		if row.Risk {
			ready[row.ID] = false
		}
	}
	// The force-merge deadline is authoritative even for historical snapshots
	// that predate the separate exit forecast field.
	for _, a := range snap.Accounts {
		if a.ForceMergeDeadline != nil && !a.ForceMergeDeadline.After(snap.At.Add(time.Duration(c.ForceMergeMinutes)*time.Minute)) {
			ready[a.ID] = false
		}
	}
	for _, t := range tasks {
		if t.Status == "ready" && t.ForceMergeSource != "" && t.Binding == snap.Binding && t.GroupID == snap.GroupID {
			ready[t.AccountID] = false
		}
	}
	decisions := map[string]restock.Decision{}
	claimed := map[string]bool{}
	for i := range tasks {
		t := &tasks[i]
		if t.Status != "ready" || t.ForceMergeAssigned || t.ForceMergeSource != "" || t.Binding != snap.Binding || t.GroupID != snap.GroupID || t.Plan != snap.NewPlan || !ready[t.AccountID] || t.ReadyAt == nil || t.ReadyAt.Before(t.CreatedAt) || t.ReadyAt.After(snap.At.Add(5*time.Second)) || t.DecisionID == "" {
			continue
		}
		d, ok := decisions[t.DecisionID]
		if !ok {
			var raw string
			err := tx.QueryRow("SELECT payload FROM pro_restock_decisions WHERE id=?", t.DecisionID).Scan(&raw)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				if err = json.Unmarshal([]byte(raw), &d); err != nil {
					return err
				}
			}
			decisions[t.DecisionID] = d
		}
		// New intents track assignments from creation, so excess capacity from
		// a mixed-rule batch can also cover a later source. Older force purchases
		// may already have served a source before assignment history was stored.
		if (d.ForceMergeNeed > 0 && !t.ForceMergeTracked) || max(d.TimeNeed, d.ConcurrencyNeed, d.ExitNeed) == 0 || d.Snapshot.Binding != snap.Binding || d.Snapshot.GroupID != snap.GroupID || !d.At.Equal(t.CreatedAt) {
			continue
		}
		original := map[string]bool{}
		wasPresent := false
		for _, a := range d.Snapshot.Accounts {
			wasPresent = wasPresent || a.ID == t.AccountID
			if a.Ready && !a.SupplyExcluded && (a.EJH || a.CardProvider == "nexovcc") && a.ForceMergeKey != "" {
				original[a.ForceMergeKey] = true
			}
		}
		if wasPresent {
			continue
		}
		for _, source := range sources {
			if claimed[source] || !original[source] {
				continue
			}
			if err := linkForceMergeReplacement(tx, t, source); err != nil {
				return err
			}
			claimed[source] = true
			ready[t.AccountID] = false
			break
		}
	}
	return nil
}
