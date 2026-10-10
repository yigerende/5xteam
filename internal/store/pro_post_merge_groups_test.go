package store

import (
	"slices"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func TestProPostMergeGroupSettingsPersistence(t *testing.T) {
	s := transferStore(t)
	v := model.DefaultProSettings()
	v.Sub2.GroupIDs = []int64{1}
	v.PostMergeGroupIDs, v.PostMergeGroupNames = []int64{7, -1, 7, 8}, []string{" A ", "bad", "ignored", "A"}
	if _, err := s.SaveProSettings(v, "password", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveProAutomationSettings(model.DefaultProSettings(), gptpay.Settings{PlanCode: "pro20"}, ""); err != nil {
		t.Fatal(err)
	}
	v, _, _, err := s.ProSettings()
	if err != nil || !slices.Equal(v.PostMergeGroupIDs, []int64{7, 8}) || !slices.Equal(v.PostMergeGroupNames, []string{"A", "A"}) || !slices.Equal(v.Sub2.GroupIDs, []int64{1}) {
		t.Fatalf("settings were lost or mixed: %+v %v", v, err)
	}
	v.PostMergeGroupIDs = nil
	if _, err := s.SaveProSettings(v, "", ""); err != nil {
		t.Fatal(err)
	}
	v, _, _, _ = s.ProSettings()
	if len(v.PostMergeGroupIDs) != 0 || len(v.PostMergeGroupNames) != 0 {
		t.Fatal("clear selection failed")
	}
}

func TestProPostMergeGroupsExportImport(t *testing.T) {
	src, dst := transferStore(t), transferStore(t)
	email := "groups@example.com"
	transferSource(t, src, email)
	ref := model.ProDownstreamRef{Provider: "sub2", URL: "https://sub.example", LoginEmail: "admin@example.com"}
	_, err := src.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.InviteStatus, p.AcceptStatus, p.TransferStatus, p.RemoveStatus = "completed", "completed", "completed", "completed"
		p.SpaceMergedOnce, p.Sub2AccountID, p.PushProvider, p.PushStatus = true, 42, "sub2", "completed"
		p.ProPostMergeGroups = &model.ProPostMergeGroups{Status: "running", GroupIDs: []int64{7, 8}, AccountID: 42, Downstream: ref, UpdatedAt: time.Now()}
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := src.ExportProAccounts([]string{email}, ref)
	if err != nil {
		t.Fatal(err)
	}
	file = transferWire(t, file)
	if _, err := dst.ImportProAccount(file.Accounts[0], file.ExportedAt, false, ref); err != nil {
		t.Fatal(err)
	}
	p, _, err := dst.MailAccountCredential(email)
	if err != nil || p.ProPostMergeGroups == nil || p.ProPostMergeGroups.Status != "pending" || !p.ProPostMergeGroups.Downstream.Matches(ref) || !slices.Equal(p.ProPostMergeGroups.GroupIDs, []int64{7, 8}) || p.ProPostMergeGroups.AccountID != 42 || p.RemoveStatus != "completed" || !p.ProMigration.Paused {
		t.Fatalf("migration lost progress: %+v %v", p.ProPostMergeGroups, err)
	}
}
