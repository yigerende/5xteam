package store

import (
	"chatgpt-space-merge/internal/herosms"
	"strings"
	"testing"
	"time"
)

func TestSMSRecoveryAndEncryptedConfig(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := herosms.Config{APIKey: "secret-key", BaseURL: "https://hero-sms.com/stubs/handler_api.php"}
	for _, id := range []string{"active", "success", "replaced"} {
		if err = s.SaveSMSActivation(id, "one@example.com", cfg); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.QueueSMSActivation("success", true); err != nil {
		t.Fatal(err)
	}
	if err = s.QueueSMSActivation("success", false); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateSMSActivation("replaced", "replaced", "", 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	var sealed string
	_ = s.db.QueryRow("SELECT encrypted_config FROM sms_activations WHERE id='active'").Scan(&sealed)
	if strings.Contains(sealed, cfg.APIKey) {
		t.Fatal("key stored in plaintext")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RecoverSMSActivations(); err != nil {
		t.Fatal(err)
	}
	items, err := s.DueSMSActivations(time.Now())
	if err != nil || len(items) != 2 {
		t.Fatalf("due: %v %v", items, err)
	}
	for _, item := range items {
		want := "cancel_pending"
		if item.ID == "success" {
			want = "finish_pending"
		}
		if item.State != want || item.Config != cfg {
			t.Fatalf("bad recovery %v", item)
		}
	}
	if err = s.UpdateSMSActivation("active", "cancel_pending", "too early", 1, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	items, err = s.DueSMSActivations(time.Now())
	if err != nil || len(items) != 1 {
		t.Fatalf("backoff ignored: %v %v", items, err)
	}
}

func TestSMSOnlyInUseActivationBlocksPurchase(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, state := range []string{"active", "cancel_pending", "finish_pending", "cleanup_failed"} {
		t.Run(state, func(t *testing.T) {
			email := state + "@example.com"
			if err := s.SaveSMSActivation(state, email, herosms.Config{APIKey: "fixture-key"}); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateSMSActivation(state, state, "", 0, time.Time{}); err != nil {
				t.Fatal(err)
			}
			blocked, err := s.HasActiveSMSActivation(email)
			if err != nil || blocked != (state == "active") {
				t.Fatalf("state=%s blocked=%v err=%v", state, blocked, err)
			}
		})
	}
}
