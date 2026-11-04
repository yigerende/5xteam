package store

import (
	"context"
	"testing"
)

func TestReadRevisionDetectsOtherConnectionsWithoutWriterLock(t *testing.T) {
	s := transferStore(t)
	first, e := s.ReadRevision(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	again, e := s.ReadRevision(t.Context())
	s.mu.Unlock()
	if e != nil || first != again {
		t.Fatal("unchanged database revision changed", e)
	}
	other, e := Open(s.path[:len(s.path)-len("state.db")])
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	if e = other.EJHPut("balance", "latest", map[string]string{"value": "1"}); e != nil {
		t.Fatal(e)
	}
	changed, e := s.ReadRevision(t.Context())
	if e != nil || changed == first {
		t.Fatal("external commit not detected", e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e = s.ReadRevision(ctx); e == nil {
		t.Fatal("cancel ignored")
	}
	recovered, e := s.ReadRevision(t.Context())
	if e != nil || recovered == changed {
		t.Fatal("connection reset identity reused", e)
	}
}
