package httpapi

import (
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestActiveTeamMembershipPreventsAdminReassignment(t *testing.T) {
	base := model.FreeAccountProfile{AdminAccountID: "admin-a", TeamAccountID: "team-a", RemoveStatus: "pending"}
	for _, test := range []struct {
		name   string
		invite string
		accept string
		remove string
		want   bool
	}{
		{name: "invite running", invite: "running", accept: "pending", want: true},
		{name: "invited waiting accept", invite: "completed", accept: "running", want: true},
		{name: "inside", invite: "completed", accept: "completed", want: true},
		{name: "not started", invite: "pending", accept: "pending", want: false},
		{name: "removed can rejoin", invite: "completed", accept: "completed", remove: "completed", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := base
			value.InviteStatus, value.AcceptStatus, value.RemoveStatus = test.invite, test.accept, test.remove
			if got := activeTeamMembership(value); got != test.want {
				t.Fatalf("activeTeamMembership()=%v, want %v for %+v", got, test.want, value)
			}
		})
	}
}

func TestTeamMembershipMutationLockSerializesInvitesAndRemovals(t *testing.T) {
	server := &Server{}
	started := make(chan struct{})
	finished := make(chan struct{})
	var orderMu sync.Mutex
	order := []string{}
	go func() {
		unlock := server.lockTeamAccountRemove("team-serial")
		defer unlock()
		orderMu.Lock()
		order = append(order, "invite-start")
		orderMu.Unlock()
		close(started)
		time.Sleep(40 * time.Millisecond)
		orderMu.Lock()
		order = append(order, "invite-end")
		orderMu.Unlock()
	}()
	<-started
	go func() {
		unlock := server.lockTeamAccountRemove("team-serial")
		defer unlock()
		orderMu.Lock()
		order = append(order, "remove")
		orderMu.Unlock()
		close(finished)
	}()
	select {
	case <-finished:
		orderMu.Lock()
		defer orderMu.Unlock()
		if len(order) < 3 || order[1] != "invite-end" {
			t.Fatalf("mutation lock did not serialize Team operations: %v", order)
		}
	case <-time.After(time.Second):
		t.Fatal("Team mutation lock did not release")
	}
}
