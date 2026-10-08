package store

import (
	"chatgpt-space-merge/internal/model"
	"testing"
)

func TestStandardRemovalSettingsAndCycleSnapshot(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings := model.DefaultAutoRotationSettings()
	if settings.SwitchBeforeRemove {
		t.Fatal("default must be off")
	}
	settings.SwitchBeforeRemove = true
	if _, err = s.SaveAutoRotationSettings(settings); err == nil {
		t.Fatal("enabled without mothers accepted")
	}
	settings.SwitchBeforeRemoveAdminIDs = []string{" mother-a ", "mother-b", "mother-a", ""}
	saved, err := s.SaveAutoRotationSettings(settings)
	if err != nil || len(saved.SwitchBeforeRemoveAdminIDs) != 2 {
		t.Fatalf("normalize: %v", err)
	}
	p, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "child@example.com", UserID: "child"}, "at")
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.PinRemovalSeatPolicy(saved); p.AdminAccountID = "mother-a" })
	if err != nil {
		t.Fatal(err)
	}
	settings.SwitchBeforeRemove = false
	settings.SwitchBeforeRemoveAdminIDs = []string{"mother-b"}
	p.PinRemovalSeatPolicy(settings)
	if !p.NeedsStandardRemoval() {
		t.Fatal("changed config mutated pinned policy")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.AutoRotationSettings().SwitchBeforeRemove {
		t.Fatal("setting lost on restart")
	}
	p, _, err = s.FreeAccountCredential(p.ID)
	if err != nil || !p.NeedsStandardRemoval() {
		t.Fatal("snapshot lost")
	}
	p, err = s.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.RemoveStatus = "completed"; p.DownstreamCleaned = true })
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.BeginFreeAccountCycle(p.ID, p.CycleID, "new-at")
	if err != nil {
		t.Fatal(err)
	}
	if p.RemovalSeatPolicy != nil || p.StandardRemoval != nil {
		t.Fatal("next cycle inherited old removal")
	}
	p.PinRemovalSeatPolicy(settings)
	if p.NeedsStandardRemoval() {
		t.Fatal("new cycle did not use new setting")
	}
	legacy := model.FreeAccountProfile{InviteStatus: "completed", AcceptStatus: "completed", AdminAccountID: "mother-a"}
	legacy.PinRemovalSeatPolicy(saved)
	if legacy.NeedsStandardRemoval() {
		t.Fatal("legacy in-flight cycle changed")
	}
}

func TestStandardRemovalLegacyFailedCycleKeepsOriginalPolicy(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "legacy@example.com", UserID: "legacy"}, "at")
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.InviteStatus = "failed"; p.AdminAccountID = "mother-a" })
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, _, err = s.FreeAccountCredential(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := model.DefaultAutoRotationSettings()
	settings.SwitchBeforeRemove, settings.SwitchBeforeRemoveAdminIDs = true, []string{"mother-a"}
	p.PinRemovalSeatPolicy(settings)
	if p.RemovalSeatPolicy == nil || p.NeedsStandardRemoval() {
		t.Fatal("legacy failed invitation inherited new removal policy")
	}
}
