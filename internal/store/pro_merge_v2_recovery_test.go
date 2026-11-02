package store

import (
	"chatgpt-space-merge/internal/model"
	"strings"
	"testing"
	"time"
)

func TestProMergeV2ProxyEncryptedAndPinnedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	email := "child@example.com"
	proxy := "http://private-user:private-pass@127.0.0.1:1234"
	got, err := s.ProMergeV2Proxy(email, at, proxy)
	if err != nil || got != proxy {
		t.Fatal(err)
	}
	got, err = s.ProMergeV2Proxy(email, at, "http://different:1234")
	if err != nil || got != proxy {
		t.Fatal("proxy rotated", err)
	}
	var raw string
	if err = s.db.QueryRow("SELECT encrypted_proxy FROM pro_merge_v2_proxies WHERE email=?", email).Scan(&raw); err != nil || strings.Contains(raw, "private") {
		t.Fatal("secret not encrypted", err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err = s.ProMergeV2Proxy(email, at, "")
	if err != nil || got != proxy {
		t.Fatal("restart lost pin", err)
	}
	got, err = s.ProMergeV2Proxy(email, at.Add(time.Hour), "")
	if err != nil || got != "" {
		t.Fatal("different workflow inherited proxy", err)
	}
}

func TestProMergeV2RecoverySettingsValidation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := model.DefaultProSettings()
	want := cfg.MergeV2RecoveryConfig()
	if !want.Enabled || want.RetrySeconds != 30 || want.MaxCycles != 2 || want.MaxAttempts != 3 {
		t.Fatal(want)
	}
	cfg.MergeV2Recovery = &model.ProMergeV2RecoverySettings{Enabled: false, RetrySeconds: 12, MaxCycles: 3, MaxAttempts: 2}
	if _, err = s.SaveProSettings(cfg, "", ""); err != nil {
		t.Fatal(err)
	}
	got, _, _, err := s.ProSettings()
	if err != nil || got.MergeV2RecoveryConfig() != *cfg.MergeV2Recovery {
		t.Fatal(got.MergeV2Recovery, err)
	}
	cfg.MergeV2Recovery.RetrySeconds = 0
	if _, err = s.SaveProSettings(cfg, "", ""); err == nil {
		t.Fatal("invalid interval saved")
	}
}

func TestProMergeV2VerificationStatusSettings(t *testing.T) {
	cfg := model.DefaultProSettings().MergeV2RecoveryConfig()
	for _, raw := range []string{"", "404", "404, 409,502", "404,404"} {
		cfg.VerificationHTTPStatuses = raw
		if err := cfg.Validate(); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"200", "600", "404,", "false", "404;409"} {
		cfg.VerificationHTTPStatuses = raw
		if err := cfg.Validate(); err == nil {
			t.Fatal("invalid status list", raw)
		}
	}
}
