package clientaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestProgressReadsCommittedCheckpointDuringWrite(t *testing.T) {
	d := testDB(t)
	c := DefaultConfig()
	source := c.SourceID()
	at := time.Now().Add(-time.Hour)
	if err := d.SaveBatch(t.Context(), source, "usage", Batch{Rows: []json.RawMessage{usage(1, at, "1")}, Next: Cursor{At: at.Format(time.RFC3339Nano), ID: 1}}); err != nil {
		t.Fatal(err)
	}
	tx, err := d.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE audit_cursors SET payload=? WHERE source=? AND entity='usage'", string(raw(Cursor{At: at.Format(time.RFC3339Nano), ID: 2})), source); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		p, err := d.Progress(ctx, c)
		if err == nil && p["cursors"].(map[string]Cursor)["usage"].ID != 1 {
			err = fmt.Errorf("progress exposed an uncommitted cursor: %v", p)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		tx.Rollback()
		<-done
		t.Fatal("progress waited for the synchronizer's write transaction")
	}
}

func TestReportSnapshotDoesNotBlockSync(t *testing.T) {
	d := testDB(t)
	c := DefaultConfig()
	at := time.Now().Add(-time.Hour)
	if err := d.SaveState(Status{Source: c.SourceID()}); err != nil {
		t.Fatal(err)
	}
	tx, err := d.read.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM audit_records").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = d.SaveBatch(ctx, c.SourceID(), "usage", Batch{Rows: []json.RawMessage{usage(1, at, "1")}}); err != nil {
		t.Fatalf("read snapshot blocked sync: %v", err)
	}
	if err = tx.QueryRow("SELECT count(*) FROM audit_records").Scan(&count); err != nil || count != 0 {
		t.Fatalf("read snapshot changed midway: %d, %v", count, err)
	}
	if _, err = tx.Exec("DELETE FROM audit_records"); err == nil {
		t.Fatal("page connection allowed a write")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = d.read.QueryRow("SELECT count(*) FROM audit_records").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	cancel()
	if _, err = d.Progress(ctx, c); err == nil {
		t.Fatal("progress ignored a canceled request")
	}
}
