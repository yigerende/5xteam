package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestLifecycleListRefreshDoesNotRewriteUnchangedTask(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	account := model.FreeAccountProfile{ID: "stable-task", Email: "stable@example.invalid", InviteStatus: "completed", ImportedAt: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)}
	first, err := s.EnsureFreeAccountLifecycleTask(account)
	if err != nil {
		t.Fatal(err)
	}
	var before, after int
	if err := s.db.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		task, err := s.EnsureFreeAccountLifecycleTask(account)
		if err != nil || !reflect.DeepEqual(task, first) {
			t.Fatalf("unchanged lifecycle differs: %v, %+v", err, task)
		}
	}
	if err := s.db.QueryRow("SELECT total_changes()").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("list refresh wrote %d unchanged records", after-before)
	}
	account.JoinMethod, account.RemoveMethod = "child_request", "child_leave"
	updated, err := s.EnsureFreeAccountLifecycleTask(account)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Steps[0].Name != "申请" || updated.Steps[1].Name != "同意" || updated.Steps[5].Name != "退出" || updated.Steps[0].Status != "completed" {
		t.Fatalf("lifecycle upgrade lost names or status: %+v", updated.Steps)
	}
	if err := s.db.QueryRow("SELECT total_changes()").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("changed lifecycle must still be persisted once, writes=%d", after-before)
	}
}

func TestAccountPaginationHonorsCanceledRequest(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.MailAccountsPageContext(ctx, "mail", "", "inside", 10, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("mail cancellation: %v", err)
	}
	if _, _, _, err := s.FreeAccountsPageContext(ctx, "inside", 10, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("team cancellation: %v", err)
	}
	if _, _, _, err := s.FreeAccountsPage("", 10, 0); err != nil {
		t.Fatalf("canceling a list request broke later queries: %v", err)
	}
}
