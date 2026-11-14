package store

import (
	"encoding/json"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestProAutoProxyEncryptedRestartAndFlowIsolation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := model.MailAccountProfile{Email: "pin@example.test", ManagementScope: "pro", ProAuto: model.ProAutoState{ID: "first"}}
	p, err = s.SaveMailAccount(p, model.MailAccountCredentials{Email: p.Email})
	if err != nil {
		t.Fatal(err)
	}
	const proxy = "http://private-user:private-password_session-first@proxy.example:8080"
	if err = s.SaveProAutoProxy(p, proxy, "primary"); err != nil {
		t.Fatal(err)
	}
	var sealed, profile string
	if err = s.db.QueryRow("SELECT encrypted_proxy FROM pro_auto_proxies WHERE email=?", p.Email).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", p.Email).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "private-") || strings.Contains(profile, "private-") {
		t.Fatal("proxy credentials leaked")
	}
	other := p
	other.ID, other.Email = "", "other@example.test"
	other, err = s.SaveMailAccount(other, model.MailAccountCredentials{Email: other.Email})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveProAutoProxy(other, "http://other.example:8080", "other"); err != nil {
		t.Fatal(err)
	}
	if got, _, e := s.ProAutoProxy(p.Email, "first"); e != nil || got != proxy {
		t.Fatal("another account changed original session", e)
	}
	if _, err = s.db.Exec("DELETE FROM mail_accounts WHERE email=?", other.Email); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, name, err := s.ProAutoProxy(strings.ToUpper(p.Email), "first")
	if err != nil || got != proxy || name != "primary" {
		t.Fatal("restart lost original session", err)
	}
	if got, _, err = s.ProAutoProxy(p.Email, "second"); err != nil || got != "" {
		t.Fatal("another flow inherited session")
	}
	p.ProAuto.ID = "second"
	raw, _ := json.Marshal(p)
	if _, err = s.db.Exec("UPDATE mail_accounts SET profile=? WHERE email=?", string(raw), p.Email); err != nil {
		t.Fatal(err)
	}
	stale := p
	stale.ProAuto.ID = "first"
	if err = s.SaveProAutoProxy(stale, proxy, "late"); err == nil {
		t.Fatal("late old flow replaced pin")
	}
	if got, _, err = s.ProAutoProxy(p.Email, "first"); err != nil || got != "" {
		t.Fatal("stale pin still usable")
	}
	if err = s.SaveProAutoProxy(p, "http://backup.example:8080", "backup"); err != nil {
		t.Fatal(err)
	}
	stale = p
	p.ProAuto.StopVersion++
	p.ProAuto.StopRequested = true
	raw, _ = json.Marshal(p)
	s.db.Exec("UPDATE mail_accounts SET profile=? WHERE email=?", string(raw), p.Email)
	if err = s.SaveProAutoProxy(stale, proxy, "late"); err == nil {
		t.Fatal("stopped worker replaced session")
	}
	if _, err = s.db.Exec("DELETE FROM mail_accounts WHERE email=?", p.Email); err != nil {
		t.Fatal(err)
	}
	var count int
	s.db.QueryRow("SELECT count(*) FROM pro_auto_proxies").Scan(&count)
	if count != 0 {
		t.Fatal("deleted account retained proxy secret")
	}
}
