package httpapi

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestPremiumUsageByAdminMatchesExistingRules(t *testing.T) {
	rng := rand.New(rand.NewSource(20260923))
	admins := []string{"", "mother-a", "mother-b", "mother-c"}
	seatTypes := []string{"", "default", "prolite", "5x", "premium"}
	statuses := []string{"", "pending", "running", "completed"}
	removeStatuses := []string{"", "pending", "completed"}
	taskStatuses := []string{"queued", "running", "failed", "completed"}
	s := &Server{}
	for round := 0; round < 200; round++ {
		accounts := make([]model.FreeAccountProfile, 30)
		for i := range accounts {
			accounts[i] = model.FreeAccountProfile{
				ID: fmt.Sprintf("account-%d", i), CycleID: fmt.Sprintf("cycle-%d", rng.Intn(3)),
				AdminAccountID: admins[rng.Intn(len(admins))], SeatType: seatTypes[rng.Intn(len(seatTypes))],
				InviteStatus: statuses[rng.Intn(len(statuses))], AcceptStatus: statuses[rng.Intn(len(statuses))],
				RemoveStatus: removeStatuses[rng.Intn(len(removeStatuses))],
			}
			if rng.Intn(5) == 0 {
				now := time.Now()
				accounts[i].RemoteRemovedAt = &now
			}
		}
		tasks := make([]model.AutoRotationTask, 45)
		for i := range tasks {
			accountID := fmt.Sprintf("account-%d", rng.Intn(len(accounts)+5))
			cycleID := ""
			if rng.Intn(2) == 0 {
				cycleID = fmt.Sprintf("cycle-%d", rng.Intn(3))
			}
			tasks[i] = model.AutoRotationTask{
				AccountID: accountID, CycleID: cycleID, AdminAccountID: admins[rng.Intn(len(admins))],
				SeatType: seatTypes[rng.Intn(len(seatTypes))], Status: taskStatuses[rng.Intn(len(taskStatuses))],
			}
		}
		usage := premiumUsageByAdmin(accounts, tasks)
		for _, adminID := range admins {
			inside, inFlight := s.premiumUsageForAdminWithTasks(adminID, accounts, tasks)
			if got := usage[adminID]; got.inside != inside || got.inFlight != inFlight {
				t.Fatalf("round=%d admin=%q new=%+v old=(%d,%d)", round, adminID, got, inside, inFlight)
			}
		}
	}
}

var benchmarkPremiumUsage int

func BenchmarkPremiumUsageByAdmin(b *testing.B) {
	const adminCount, accountCount, taskCount = 20, 5000, 1000
	accounts := make([]model.FreeAccountProfile, accountCount)
	for i := range accounts {
		accounts[i] = model.FreeAccountProfile{ID: fmt.Sprintf("account-%d", i), AdminAccountID: fmt.Sprintf("mother-%d", i%adminCount), SeatType: "prolite", InviteStatus: "completed", AcceptStatus: "pending"}
	}
	tasks := make([]model.AutoRotationTask, taskCount)
	for i := range tasks {
		tasks[i] = model.AutoRotationTask{AccountID: fmt.Sprintf("account-%d", i), AdminAccountID: fmt.Sprintf("mother-%d", i%adminCount), SeatType: "prolite", Status: "queued"}
	}
	b.Run("original_per_mother", func(b *testing.B) {
		s := &Server{}
		for i := 0; i < b.N; i++ {
			for admin := 0; admin < adminCount; admin++ {
				_, inFlight := s.premiumUsageForAdminWithTasks(fmt.Sprintf("mother-%d", admin), accounts, tasks)
				benchmarkPremiumUsage = inFlight
			}
		}
	})
	b.Run("single_pass", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchmarkPremiumUsage = premiumUsageByAdmin(accounts, tasks)["mother-0"].inFlight
		}
	})
}
