package httpapi

import (
	"context"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestRotationCapacityExcludesDisabledMotherWithoutChangingWork(t *testing.T) {
	admins := []model.AdminAccountProfile{{ID: "on"}, {ID: "off", RotationDisabled: true}}
	snapshots := map[string]model.AdminSeatCapacity{
		"on":      {Premium: model.AdminSeatBucket{Total: 2}},
		"off":     {Premium: model.AdminSeatBucket{Total: 10}},
		"deleted": {Premium: model.AdminSeatBucket{Total: 100}},
	}
	accounts := []model.FreeAccountProfile{
		{ID: "on-child", AdminAccountID: "on", SeatType: "prolite", AcceptStatus: "completed", Quota7D: &model.FreeQuotaWindow{UsedPercent: 80}},
		{ID: "off-child", AdminAccountID: "off", SeatType: "prolite", AcceptStatus: "completed", Quota7D: &model.FreeQuotaWindow{UsedPercent: 0}},
		{ID: "off-pending", AdminAccountID: "off", SeatType: "prolite", InviteStatus: "running"},
	}
	total, inside, found := premiumSeatSnapshot(admins, snapshots, accounts)
	if total != 2 || inside != 1 || found != 1 {
		t.Fatalf("total=%d inside=%d found=%d", total, inside, found)
	}
	if avg := averageFreeQuota(rotationCapacityAccounts(admins, accounts), total); avg != 10 {
		t.Fatal("disabled quota affected trigger", avg)
	}
	if accounts[2].InviteStatus != "running" || accounts[1].AcceptStatus != "completed" || snapshots["off"].Premium.Total != 10 {
		t.Fatal("disabled work or snapshot modified")
	}
	admins[1].RotationDisabled = false
	total, inside, found = premiumSeatSnapshot(admins, snapshots, accounts)
	if total != 12 || inside != 2 || found != 2 {
		t.Fatal("re-enable did not restore saved capacity")
	}
	admins[0].RotationDisabled = true
	admins[1].RotationDisabled = true
	total, inside, found = premiumSeatSnapshot(admins, snapshots, accounts)
	if total != 0 || inside != 0 || found != 0 {
		t.Fatal("all disabled still supplies capacity")
	}
}

func TestRotationDisabledCapacityTriggerAndPagedSummary(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	on := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "enabled", TeamAccountID: "on-team"}, "at", "")
	time.Sleep(20 * time.Millisecond)
	off := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "disabled", TeamAccountID: "off-team"}, "at", "")
	for _, a := range []model.AdminAccountProfile{on, off} {
		if err := st.SaveAdminCapacitySnapshot(a.ID, model.AdminSeatCapacity{Premium: model.AdminSeatBucket{Total: 2}}); err != nil {
			t.Fatal(err)
		}
	}
	for i, admin := range []model.AdminAccountProfile{on, on, off, off} {
		name := []string{"on-in", "on-flight", "off-in", "off-flight"}[i]
		p, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: name + "@example.com", UserID: name}, "at")
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
			p.AdminAccountID = admin.ID
			p.TeamAccountID = admin.TeamAccountID
			p.SeatType = "prolite"
			p.InviteStatus = "completed"
			if i%2 == 0 {
				p.AcceptStatus = "completed"
				p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 100}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	setRotationDisabled(t, s, off.ID, true)
	for _, trigger := range []string{"manual", "automatic"} {
		run, _, err := s.startAutoRotation(context.Background(), trigger)
		if err != nil || run.Status != "skipped" || run.SeatTotal != 2 || run.SeatRemaining != 1 || run.ReservedSeats != 1 {
			t.Fatalf("wrong decision: %+v %v", run, err)
		}
		if len(st.AutoRotationTasks(run.ID)) != 0 {
			t.Fatal("disabled seats scheduled tasks")
		}
	}
	_, total, summary, err := st.FreeAccountsPage("", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || summary.Inside != 2 || summary.InvitePending != 2 {
		t.Fatal("disabled accounts disappeared from workflow counts", summary)
	}
	if summary.SeatUsageByAdmin[on.ID].InsidePremium != 1 || summary.SeatUsageByAdmin[off.ID].InsidePremium != 1 {
		t.Fatal("page-independent mother usage missing")
	}
	if summary.PendingSeatsByAdmin[on.ID]["premium"] != 1 || summary.PendingSeatsByAdmin[off.ID]["premium"] != 1 {
		t.Fatal("in-flight usage missing")
	}
	setRotationDisabled(t, s, on.ID, true)
	run, _, err := s.startAutoRotation(context.Background(), "manual")
	if err != nil || run.SeatTotal != 0 || run.Status != "skipped" {
		t.Fatal("all-disabled triggered rotation")
	}
	if len(st.AdminCapacitySnapshots()) != 2 {
		t.Fatal("disabled snapshots erased")
	}
}
