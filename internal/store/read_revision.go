package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// ReadRevision uses one persistent, query-only connection. SQLite's data_version
// is only comparable on that same connection and changes on commits made by any
// other connection, including operators and background jobs. It is a display
// invalidation token, never a payment/admission decision.
func (s *Store) ReadRevision(ctx context.Context) (string, error) {
	s.revisionMu.Lock()
	defer s.revisionMu.Unlock()
	return s.readRevisionLocked(ctx)
}

func (s *Store) readRevisionLocked(ctx context.Context) (string, error) {
	if s.revisionDB == nil {
		path := strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(filepath.ToSlash(s.path))
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)")
		if err != nil {
			return "", err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		s.revisionDB = db
		s.revisionEpoch++
		if s.revisionIdentity == "" {
			s.revisionIdentity = rand.Text()
		}
	}
	var version int64
	if err := s.revisionDB.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
		_ = s.revisionDB.Close()
		s.revisionDB = nil
		return "", err
	}
	return fmt.Sprintf("%s:%d:%d", s.revisionIdentity, s.revisionEpoch, version), nil
}
