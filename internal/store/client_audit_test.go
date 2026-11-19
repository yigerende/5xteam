package store

import (
	"chatgpt-space-merge/internal/clientaudit"
	"strings"
	"testing"
)

func TestClientAuditSettingsEncrypted(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	c := clientaudit.DefaultConfig()
	if _, e = s.SaveClientAuditSettings(c, clientaudit.Secrets{DBPassword: "fixture-private"}); e != nil {
		t.Fatal(e)
	}
	var p, key string
	if e = s.db.QueryRow("SELECT profile,secrets FROM client_audit_settings").Scan(&p, &key); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(p+key, "fixture-private") {
		t.Fatal("secret stored as plaintext")
	}
	if _, e = s.SaveClientAuditSettings(c, clientaudit.Secrets{}); e != nil {
		t.Fatal(e)
	}
	_, sec, e := s.ClientAuditSettings()
	if e != nil || sec.DBPassword != "fixture-private" {
		t.Fatal(sec, e)
	}
}
