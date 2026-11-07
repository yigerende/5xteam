package store

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"chatgpt-space-merge/internal/ejh"
)

func TestEJHSigningEncryptedPersistentAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ejh.DefaultSettings()
	cfg.AppKey = "fixture"
	cfg.Enabled = true
	if _, err = s.EnsureEJHSigningKey(""); err == nil {
		t.Fatal("unsaved credentials accepted")
	}
	if _, err = s.SaveEJHSettings(cfg, "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	pubs := make(chan string, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.EnsureEJHSigningKey("")
			if e != nil {
				t.Error(e)
				return
			}
			pubs <- v.SigningPublicKey
		}()
	}
	wg.Wait()
	close(pubs)
	public := ""
	for p := range pubs {
		if public != "" && p != public {
			t.Fatal("generation rotated saved key")
		}
		public = p
	}
	v, secret, err := s.EJHSettings()
	if err != nil || secret != "fixture-secret" || !v.SigningKeyPresent || v.SigningPublicKey != public || v.SigningPrivateKey == "" {
		t.Fatal("saved key not loaded", err)
	}
	private := v.SigningPrivateKey
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), private) || strings.Contains(string(raw), "private_key") {
		t.Fatal("private key in JSON")
	}
	var sealed, profile string
	if err = s.db.QueryRow(`SELECT sealed FROM ejh_records WHERE kind='signing_key'`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, private) || strings.Contains(sealed, "private_key") {
		t.Fatal("key plaintext")
	}
	decrypted, err := s.decrypt(sealed)
	if err != nil || !strings.Contains(decrypted, private) {
		t.Fatal("encrypted record cannot be recovered")
	}
	v.SigningPrivateKey = "spoofed"
	v.SigningPublicKey = "spoofed"
	v.SigningKeyPresent = false
	v.AlertHours = 48
	if v, err = s.SaveEJHSettings(v, ""); err != nil || v.SigningPrivateKey != private || v.SigningPublicKey != public {
		t.Fatal("settings save changed key", err)
	}
	if err = s.db.QueryRow(`SELECT profile FROM ejh_settings`).Scan(&profile); err != nil || strings.Contains(profile, private) {
		t.Fatal("plaintext settings")
	}
	reader, err := s.ProListReader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rv, _, err := reader.EJHSettings()
	reader.Close()
	if err != nil || rv.SigningPrivateKey != private {
		t.Fatal("read-only detail lost signing context", err)
	}
	if _, err = s.EnsureEJHSigningKey(private); err != nil {
		t.Fatal("same import must be idempotent", err)
	}
	other, _, _ := ejh.GenerateSigningKey()
	if _, err = s.EnsureEJHSigningKey(other); err == nil {
		t.Fatal("replaced registered key")
	}
	for _, change := range []func(*ejh.Settings){func(c *ejh.Settings) { c.AppKey = "other" }, func(c *ejh.Settings) { c.URL = "https://other.invalid" }} {
		next := v
		change(&next)
		if _, err = s.SaveEJHSettings(next, ""); err == nil {
			t.Fatal("key reused with different merchant")
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, _, err = s.EJHSettings()
	if err != nil || v.SigningPrivateKey != private || v.SigningPublicKey != public || v.AlertHours != 48 {
		t.Fatal("restart lost key or config", err)
	}
	rotated, err := s.RegenerateEJHSigningKey()
	if err != nil || rotated.SigningPublicKey == public || rotated.SigningPrivateKey == private || rotated.SigningPrivateKey == "" {
		t.Fatal("explicit rotation failed", err)
	}
	// EJH reissues AppSecret after public-key registration; saving it must
	// preserve the new key and unrelated merchant/automation settings.
	if _, err = s.SaveEJHSettings(rotated, "new-issuer-secret"); err != nil {
		t.Fatal(err)
	}
	v, secret, err = s.EJHSettings()
	if err != nil || secret != "new-issuer-secret" || v.SigningPrivateKey != rotated.SigningPrivateKey || v.AlertHours != 48 {
		t.Fatal("new issuer secret changed signing identity", err)
	}
	again, err := s.EnsureEJHSigningKey("")
	if err != nil || again.SigningPublicKey != rotated.SigningPublicKey {
		t.Fatal("refresh rotated key", err)
	}
}

func TestEJHSigningImportAndTamper(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := ejh.DefaultSettings()
	cfg.AppKey = "fixture"
	if _, err = s.SaveEJHSettings(cfg, "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	legacy, _, err := s.EJHSettings()
	if err != nil || legacy.SigningKeyPresent || legacy.SigningPrivateKey != "" {
		t.Fatal("legacy changed")
	}
	if _, err = s.EnsureEJHSigningKey("private-invalid-fixture"); err == nil || strings.Contains(err.Error(), "private-invalid-fixture") {
		t.Fatal("invalid import not rejected safely")
	}
	private, public, _ := ejh.GenerateSigningKey()
	v, err := s.EnsureEJHSigningKey(private)
	if err != nil || v.SigningPublicKey != public {
		t.Fatal("import failed", err)
	}
	if _, err = s.db.Exec(`UPDATE ejh_records SET sealed='broken' WHERE kind='signing_key'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EJHSettings(); err == nil {
		t.Fatal("corrupt key downgraded to unsigned")
	}
}
