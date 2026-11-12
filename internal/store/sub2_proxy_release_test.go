package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestSub2ProxyReleaseAtomicPersistenceClaimAndRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	now := time.Now().UTC()
	due := now.Add(time.Minute)
	for i := 1; i <= 20; i++ {
		b := model.Sub2ProxyBinding{ID: fmt.Sprint(i), Endpoint: "https://one.example", TargetID: "encrypted-target", Scope: "pro:one", AccountID: int64(i), ProxyID: 7, Minutes: 1, BoundAt: now, DueAt: &due}
		if err = s.CompleteSub2ProxyBinding(fmt.Sprint(i), `{"secret":"test-only"}`, fmt.Sprint(i), int64(i), b); err != nil {
			t.Fatal(err)
		}
	}
	claimed := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, e := s.ClaimSub2ProxyReleases(now.Add(time.Second))
			if e != nil {
				t.Error(e)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, b := range rows {
				if claimed[b.ID] {
					t.Error("double claim", b.ID)
				}
				claimed[b.ID] = true
				if b.TargetID != "encrypted-target" || b.Endpoint != "https://one.example" {
					t.Error("lost private target")
				}
			}
		}()
	}
	wg.Wait()
	if len(claimed) != 20 {
		t.Fatal(len(claimed))
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ClaimSub2ProxyReleases(now.Add(2 * time.Minute))
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	b := rows[0]
	if !b.BoundAt.Equal(now) || !b.DueAt.Equal(due) {
		t.Fatal("restart reset timer")
	}
	b.State = "unbound"
	b.NextAt = 0
	if err = s.FinishSub2ProxyRelease(b); err != nil {
		t.Fatal(err)
	}
	stale := b
	stale.Revision--
	stale.State = "retry"
	if err = s.FinishSub2ProxyRelease(stale); err == nil {
		t.Fatal("stale claimant overwrote success")
	}
	if err = s.RetrySub2ProxyRelease(b.ID, time.Now()); err == nil {
		t.Fatal("completed binding reopened")
	}
	// Duplicate remote confirmation cannot restart an already released binding.
	b.DueAt = nil
	b.Minutes = 0
	b.BoundAt = time.Now()
	if err = s.CompleteSub2ProxyBinding("duplicate", `{}`, "duplicate", b.AccountID, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.Sub2ProxyBindings([]string{b.ID})
	if err != nil || got[b.ID].State != "unbound" || !got[b.ID].BoundAt.Equal(now) {
		t.Fatal(got, err)
	}
}

func TestSub2ProxyReleaseSettingsValidation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, minutes := range []int{0, 1, 30, 43200} {
		v := model.DefaultSub2Settings()
		v.ProxyReleaseMinutes = minutes
		if _, err = s.SaveSub2Settings(v, ""); err != nil {
			t.Fatal(err)
		}
		got, _, err := s.Sub2Settings()
		if err != nil || got.ProxyReleaseMinutes != minutes {
			t.Fatal(got, err)
		}
		p := model.DefaultProSettings()
		p.Sub2 = v
		if _, err = s.SaveProSettings(p, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, minutes := range []int{-1, 43201} {
		siwc := model.DefaultProSIWCSettings()
		siwc.Sub2.ProxyReleaseMinutes = minutes
		if _, err = s.SaveProSIWCSettings(siwc, ""); err == nil {
			t.Fatal("bad SIWC retention accepted")
		}
		v := model.DefaultSub2Settings()
		v.ProxyReleaseMinutes = minutes
		if _, err = s.SaveSub2Settings(v, ""); err == nil {
			t.Fatal("bad retention accepted")
		}
		p := model.DefaultProSettings()
		p.Sub2 = v
		if _, err = s.SaveProSettings(p, "", ""); err == nil {
			t.Fatal("bad pro retention accepted")
		}
	}
}

func TestSub2ProxyReleaseFreeReimportPreservesBinding(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := model.FreeAccountProfile{UserID: "proxy-release-user", Email: "proxy-release@example.test"}
	saved, _, err := s.SaveImportedFreeAccount(p, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateFreeAccount(saved.ID, func(a *model.FreeAccountProfile) {
		a.Sub2ProxyBindingID = "original"
		a.Sub2AccountID = 88
		a.PushProvider = "sub2"
	})
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := s.SaveImportedFreeAccount(p, "new-fixture-token")
	if err != nil || again.Sub2ProxyBindingID != "original" || again.Sub2AccountID != 88 {
		t.Fatal("reimport lost original binding", again.Sub2ProxyBindingID, err)
	}
}
