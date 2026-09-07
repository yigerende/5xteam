package httpapi

import (
	"context"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestAutoRotationSkipsOverlappingScheduledRun(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.autoRunning = true
	run, started, err := s.startAutoRotation(context.Background(), "automatic")
	if err != nil {
		t.Fatal(err)
	}
	if started || run.Status != "running" {
		t.Fatalf("overlapping run was not rejected: started=%v run=%+v", started, run)
	}
	s.autoRunning = false
}

func TestAutoRotationSkipsWithoutCapacitySnapshot(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveAutoRotationSettings(model.AutoRotationSettings{Enabled: true, ThresholdPercent: 50, IntervalSeconds: 60, Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
	run, started, err := s.startAutoRotation(context.Background(), "manual")
	if err != nil {
		t.Fatal(err)
	}
	if !started || run.Status != "skipped" || run.Reason != "没有可用的席位统计快照，请先在 Team 账号管理页面刷新 5x 席位" {
		t.Fatalf("unexpected no-snapshot decision: started=%v run=%+v", started, run)
	}
}

func TestAverageFreeQuotaUsesOnlyInsideAccountsAndSeatTotal(t *testing.T) {
	usedA, usedB, usedRemoved := 20.0, 60.0, 0.0
	got := averageFreeQuota([]model.FreeAccountProfile{
		{AcceptStatus: "completed", RemoveStatus: "pending", Quota7D: &model.FreeQuotaWindow{UsedPercent: usedA}},
		{AcceptStatus: "completed", RemoveStatus: "pending", Quota7D: &model.FreeQuotaWindow{UsedPercent: usedB}},
		{AcceptStatus: "completed", RemoveStatus: "completed", Quota7D: &model.FreeQuotaWindow{UsedPercent: usedRemoved}},
		{AcceptStatus: "pending", Quota7D: &model.FreeQuotaWindow{UsedPercent: 0}},
	}, 4)
	if got != 30 {
		t.Fatalf("average=%v, want 30", got)
	}
}

func TestAverageFreeQuotaReturnsUnknownWithoutQuota(t *testing.T) {
	if got := averageFreeQuota([]model.FreeAccountProfile{{AcceptStatus: "completed"}}, 4); got >= 0 {
		t.Fatalf("expected unknown, got %v", got)
	}
}

func TestAutoRotationPlanLimitsByMaxSeatsAndCandidates(t *testing.T) {
	cases := []struct {
		name                                    string
		max, remote, reserved, candidates, want int
	}{
		{"max first", 3, 10, 2, 20, 3},
		{"seat shortage", 10, 4, 3, 20, 1},
		{"candidate shortage", 10, 10, 0, 2, 2},
		{"zero max", 0, 5, 1, 20, 4},
		{"reserved over remote", 10, 1, 4, 20, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoRotationPlan(tc.max, tc.remote, tc.reserved, tc.candidates); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestAutoRotationDecisionSkipsWhenThresholdReachedButNoSeats(t *testing.T) {
	if run, reason := autoRotationDecision(48, 50, 0); run || reason != "已达到额度阈值，但当前没有可用的 5x 剩余席位" {
		t.Fatalf("unexpected decision: run=%v reason=%q", run, reason)
	}
	if run, _ := autoRotationDecision(48, 50, 2); !run {
		t.Fatal("expected run when seats are available")
	}
	if run, _ := autoRotationDecision(60, 50, 2); run {
		t.Fatal("should skip above threshold")
	}
}

func TestPremiumSeatSnapshotCountsOnlyConfiguredAdminsAndInsidePremium(t *testing.T) {
	admins := []model.AdminAccountProfile{{ID: "a"}, {ID: "b"}, {ID: "missing"}}
	snapshots := map[string]model.AdminSeatCapacity{
		"a":      {Premium: model.AdminSeatBucket{Total: 9}},
		"b":      {Premium: model.AdminSeatBucket{Total: 4}},
		"orphan": {Premium: model.AdminSeatBucket{Total: 100}},
	}
	accounts := []model.FreeAccountProfile{
		{AcceptStatus: "completed", RemoveStatus: "pending", SeatType: "prolite"},
		{AcceptStatus: "completed", RemoveStatus: "pending", SeatType: "5x"},
		{AcceptStatus: "completed", RemoveStatus: "pending", SeatType: "default"},
		{AcceptStatus: "completed", RemoveStatus: "completed", SeatType: "prolite"},
		{AcceptStatus: "pending", RemoveStatus: "pending", SeatType: "prolite"},
	}
	total, inside, found := premiumSeatSnapshot(admins, snapshots, accounts)
	if total != 13 || inside != 2 || found != 2 {
		t.Fatalf("got total=%d inside=%d found=%d", total, inside, found)
	}
}

func TestAvailablePremiumFromSnapshotIncludesInFlightReservations(t *testing.T) {
	if got := availablePremiumFromSnapshot(9, 8, 1); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
	if got := availablePremiumFromSnapshot(9, 8, 0); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	if got := availablePremiumFromSnapshot(2, 4, 0); got != 0 {
		t.Fatalf("negative availability should clamp to zero, got %d", got)
	}
}

func TestAutoRotationNeverSelectsDeadAccounts(t *testing.T) {
	ready := model.FreeAccountProfile{ImportMode: "", InviteStatus: "pending", AcceptStatus: "pending", RemoveStatus: "pending"}
	if !eligibleAutoRotationAccount(ready) {
		t.Fatal("normal queued account should be eligible")
	}
	ready.Dead = true
	if eligibleAutoRotationAccount(ready) {
		t.Fatal("dead Team account must not be selected again")
	}
	if eligibleAutoRotationMail(model.MailAccountProfile{ChatGPTStatus: "dead"}) {
		t.Fatal("mail account with dead ChatGPT status must not be selected")
	}
	if eligibleAutoRotationMail(model.MailAccountProfile{RegistrationStatus: "dead"}) {
		t.Fatal("legacy dead mail status must not be selected")
	}
	if !eligibleAutoRotationMail(model.MailAccountProfile{RegistrationStatus: "success"}) {
		t.Fatal("normal registered mailbox should remain eligible")
	}
}
