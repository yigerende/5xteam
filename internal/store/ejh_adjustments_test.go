package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func adjustmentEvidence(t *testing.T, s *Store) EJHAdjustmentEvidence {
	t.Helper()
	v, e := s.EJHAccountingVersion(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return EJHAdjustmentEvidence{Version: v, RawGap: "-132.07", GapBefore: "-132.07", TotalBefore: "0.00", CalculatedAt: time.Now(), Snapshot: json.RawMessage(`{"cost":"6339.94"}`)}
}
func TestEJHAdjustmentAtomicConcurrentPersistence(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { s.Close() }()
	evidence := adjustmentEvidence(t, s)
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, changed := 0, 0
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.AppendEJHAdjustment(context.Background(), evidence, fmt.Sprintf("test-concurrent-%03d", i), "admin", "历史差额平账", "132.07")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else if errors.Is(err, ErrEJHAdjustmentChanged) {
				changed++
			} else {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if success != 1 || changed != 23 {
		t.Fatal(success, changed)
	}
	rows, e := s.EJHAdjustments(context.Background(), 1, 10)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
	row := rows[0]
	if row.GapAfter != "0.00" || row.TotalAfter != "132.07" || row.RawGap != "-132.07" {
		t.Fatal(row)
	}
	for i := 0; i < 5; i++ {
		if _, e = s.AppendEJHAdjustment(context.Background(), evidence, row.RequestID, "admin", row.Note, row.Amount); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.AppendEJHAdjustment(context.Background(), evidence, row.RequestID, "admin", "不同备注", row.Amount); e == nil {
		t.Fatal("request reused")
	}
	s.Close()
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	summary, e := s.EJHAdjustmentSummary()
	if e != nil || summary.Amount != "132.07" || summary.Count != 1 {
		t.Fatal(summary, e)
	}
	persisted, e := s.EJHAdjustmentByRequest(row.RequestID)
	if e != nil || persisted.GapAfter != "0.00" || string(persisted.Snapshot) != string(evidence.Snapshot) {
		t.Fatal(persisted, e)
	}
	evidence = adjustmentEvidence(t, s)
	evidence.TotalBefore = "132.07"
	evidence.GapBefore = "0.00"
	reversed, e := s.AppendEJHAdjustment(context.Background(), evidence, "test-reversal-0001", "admin", "更正前一笔", "-132.07")
	if e != nil || reversed.GapAfter != "-132.07" || reversed.TotalAfter != "0.00" {
		t.Fatal(reversed, e)
	}
	rows, e = s.EJHAdjustments(context.Background(), 1, 1)
	if e != nil || len(rows) != 1 || rows[0].ID != reversed.ID {
		t.Fatal(rows, e)
	}
}
func TestEJHAdjustmentRejectIncompleteOrChanged(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	evidence := adjustmentEvidence(t, s)
	for _, v := range []string{"0.00", "1.234", "NaN", "1e4", "", "1000000000000.00"} {
		if _, e = s.AppendEJHAdjustment(context.Background(), evidence, "test-validation-0001", "admin", "test", v); e == nil {
			t.Fatal(v)
		}
	}
	bad := evidence
	bad.RawGap = ""
	if _, e = s.AppendEJHAdjustment(context.Background(), bad, "test-validation-0001", "admin", "test", "132.07"); e == nil {
		t.Fatal("unknown accepted")
	}
	s.SaveEJHRechargeLedger("100.00")
	if _, e = s.AppendEJHAdjustment(context.Background(), evidence, "test-validation-0001", "admin", "test", "132.07"); !errors.Is(e, ErrEJHAdjustmentChanged) {
		t.Fatal(e)
	}
	summary, _ := s.EJHAdjustmentSummary()
	if summary.Count != 0 || summary.Amount != "0.00" {
		t.Fatal(summary)
	}
}
