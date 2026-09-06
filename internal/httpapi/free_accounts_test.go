package httpapi

import (
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestManualFreeAccountStageUpdatesProgress(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	profile := model.FreeAccountProfile{
		InviteStatus: "completed", AcceptStatus: "failed", OAuthStatus: "pending",
		PushStatus: "pending", QuotaStatus: "pending", RemoveStatus: "pending", LastError: "old failure",
	}

	applyManualFreeAccountStage(&profile, "accept", "completed", "", now)
	if profile.AcceptStatus != "completed" || profile.JoinedAt == nil || !profile.JoinedAt.Equal(now) || profile.Status != "joined" || profile.LastError != "" {
		t.Fatalf("manual completion was not applied: %+v", profile)
	}

	applyManualFreeAccountStage(&profile, "oauth", "failed", "manual oauth failed", now)
	if profile.OAuthStatus != "failed" || profile.Status != "oauth_failed" || profile.LastError != "manual oauth failed" || profile.OAuthReadyAt != nil {
		t.Fatalf("manual failure was not applied: %+v", profile)
	}

	applyManualFreeAccountStage(&profile, "oauth", "pending", "", now)
	if profile.OAuthStatus != "pending" || profile.Status != "joined" || profile.LastError != "" {
		t.Fatalf("manual reset was not applied: %+v", profile)
	}

	applyManualFreeAccountStage(&profile, "push", "completed", "", now)
	if profile.PushStatus != "completed" || profile.PushedAt == nil || profile.Status != "monitoring" {
		t.Fatalf("manual push completion was not applied: %+v", profile)
	}
}

func TestFreeAccountJoinSkipsCompletedStages(t *testing.T) {
	tests := []struct {
		name                   string
		invite, accept         string
		wantInvite, wantAccept bool
	}{
		{name: "new account", invite: "pending", accept: "pending", wantInvite: true, wantAccept: true},
		{name: "manually invited", invite: "completed", accept: "pending", wantInvite: false, wantAccept: true},
		{name: "already joined", invite: "completed", accept: "completed", wantInvite: false, wantAccept: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotInvite, gotAccept := freeAccountJoinSteps(model.FreeAccountProfile{InviteStatus: tt.invite, AcceptStatus: tt.accept})
			if gotInvite != tt.wantInvite || gotAccept != tt.wantAccept {
				t.Fatalf("steps = invite:%v accept:%v, want invite:%v accept:%v", gotInvite, gotAccept, tt.wantInvite, tt.wantAccept)
			}
		})
	}
}
