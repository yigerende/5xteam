package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProMergeV2ReloadsMotherTokenAfterChildLogin(t *testing.T) {
	f := mergeV2Fixture(t)
	f.existingMember = true
	// The invitation succeeded earlier, but the workflow starts with an old AT.
	if _, err := f.s.store.SaveAdminAccountCredentials(f.admin, "old-mother-token", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.ProMergeV2 = &model.ProMergeV2{AdminID: f.admin.ID, TeamID: f.admin.TeamAccountID, Steps: map[string]string{"invite": "completed"}, StartedAt: time.Now()}
	}); err != nil {
		t.Fatal(err)
	}
	logins := 0
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
		logins++
		if _, err := f.s.store.SaveAdminAccountCredentials(f.admin, "mother-token", ""); err != nil {
			return nil, err
		}
		return mockMergeV2Submit(ctx)
	}
	p, err := f.s.performProMergeV2(t.Context(), f.email)
	if err != nil || !p.MergeV2Completed() || logins != 1 || f.calls["remove"] != 1 || f.calls["invite"] != 0 {
		t.Fatalf("resume failed: err=%v state=%+v logins=%d calls=%v", err, p.ProMergeV2, logins, f.calls)
	}
}

func TestProMergeV2ExpiredMotherAfterAcceptancePreservesResume(t *testing.T) {
	f := mergeV2Fixture(t)
	logins := 0
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
		logins++
		result, err := mockMergeV2Submit(ctx)
		if err != nil {
			return nil, err
		}
		expired := time.Now().Add(-time.Minute)
		admin := f.admin
		admin.AccessTokenExpiresAt = &expired
		_, err = f.s.store.SaveAdminAccountCredentials(admin, "mother-token", "")
		return result, err
	}
	p, err := f.s.performProMergeV2(t.Context(), f.email)
	if err == nil || !strings.Contains(err.Error(), "母号 AT 已过期") || !p.MergeV2Accepted() || p.MergeV2Completed() || f.calls["remove"] != 0 {
		t.Fatalf("expired mother used or acceptance lost: err=%v state=%+v calls=%v", err, p.ProMergeV2, f.calls)
	}
	acceptedAt := *p.ProMergeV2.AcceptedAt
	if _, err = f.s.store.SaveAdminAccountCredentials(f.admin, "mother-token", ""); err != nil {
		t.Fatal(err)
	}
	dueRecovery(t, f)
	p, err = f.s.performProMergeV2(t.Context(), f.email)
	if err != nil || !p.MergeV2Completed() || logins != 1 || f.calls["invite"] != 1 || f.calls["remove"] != 1 || !p.ProMergeV2.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("accepted request repeated or resume failed: err=%v state=%+v logins=%d calls=%v", err, p.ProMergeV2, logins, f.calls)
	}
}

func TestProMergeV2MotherTeamChangeBeforeRemovalIsBlocked(t *testing.T) {
	f := mergeV2Fixture(t)
	f.s.proMergeV2Login = func(ctx context.Context, _ string) (map[string]any, error) {
		result, err := mockMergeV2Submit(ctx)
		if err != nil {
			return nil, err
		}
		admin := f.admin
		admin.TeamAccountID = "another-team"
		_, err = f.s.store.SaveAdminAccountCredentials(admin, "mother-token", "")
		return result, err
	}
	p, err := f.s.performProMergeV2(t.Context(), f.email)
	if err == nil || !strings.Contains(err.Error(), "团队不一致") || !p.MergeV2Accepted() || p.MergeV2Completed() || f.calls["remove"] != 0 {
		t.Fatalf("changed mother team used: err=%v state=%+v calls=%v", err, p.ProMergeV2, f.calls)
	}
}
