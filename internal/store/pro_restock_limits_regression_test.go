package store

import (
	"chatgpt-space-merge/internal/restock"
	"fmt"
	"testing"
	"time"
)

func TestRestockLimitsIndexedMatchesHistoricalRules(t *testing.T) {
	s := transferStore(t)
	now := time.Date(2026, 11, 21, 0, 20, 0, 0, time.FixedZone("CST", 28800))
	day := time.Date(2026, 11, 21, 0, 0, 0, 0, now.Location())
	for i, status := range []string{"ready", "failed", "cancelled", "running", "unknown", "attention", "reserved"} {
		for j, age := range []time.Duration{10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 72 * time.Hour} {
			for variant := 0; variant < 3; variant++ {
				at := now.Add(-age)
				task := restock.Task{ID: fmt.Sprintf("%d-%d-%d", i, j, variant), Email: fmt.Sprintf("%d-%d-%d@test.invalid", i, j, variant), Status: status, CreatedAt: at, UpdatedAt: at, Cost: 17, ManagedExternally: variant == 1}
				if variant == 2 {
					task.StoppedAt, task.SubmittedAt = &at, &at
				}
				if _, err := s.db.Exec(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`, task.ID, task.Email, status, formatTime(at), task.Cost, mustJSON(task)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	got, err := s.ProRestockLimits(now)
	if err != nil {
		t.Fatal(err)
	}
	var want restock.Limits
	err = s.db.QueryRow(`SELECT coalesce(sum(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),coalesce(sum(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),coalesce(sum(CASE WHEN created_at>=? OR status NOT IN ('ready','failed','cancelled') THEN cost ELSE 0 END),0),coalesce(sum(CASE WHEN created_at>=? OR status NOT IN ('ready','failed','cancelled') THEN cost ELSE 0 END),0) FROM pro_restock_tasks WHERE (status!='cancelled' OR (json_extract(payload,'$.stopped_at') IS NOT NULL AND json_extract(payload,'$.submitted_at') IS NOT NULL)) AND coalesce(json_extract(payload,'$.managed_externally'),0)=0`, formatTime(now.Add(-time.Hour)), formatTime(day), formatTime(now.Add(-time.Hour)), formatTime(day)).Scan(&want.HourCount, &want.DayCount, &want.HourCost, &want.DayCost)
	if err != nil {
		t.Fatal(err)
	}
	if got.HourCount != want.HourCount || got.DayCount != want.DayCount || got.HourCost != want.HourCost || got.DayCost != want.DayCost {
		t.Fatalf("indexed limits changed accounting: got=%+v want=%+v", got, want)
	}
}
