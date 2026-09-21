package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestProBatchDeleteOnlySelectedAndReportsFailures(t *testing.T) {
	f := newProExecutionFixture(t)
	for _, email := range []string{"busy@example.com", "keep@example.com", "mail@example.com"} {
		if _, err := f.s.store.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{}); err != nil {
			t.Fatal(err)
		}
		if email != "mail@example.com" {
			f.s.store.UpdateMailAccountManagementScope(email, "pro")
		}
	}
	unlock := f.s.lockProAccount("busy@example.com")
	defer unlock()
	w := f.request(f.s.deleteProAccounts, "POST", "/api/pro-accounts/delete", `{"emails":[" PRO-LOG@example.com ","pro-log@example.com","busy@example.com","mail@example.com","missing@example.com"]}`)
	var payload struct {
		Data struct {
			Items                    []proDeleteResult `json:"items"`
			Total, Succeeded, Failed int
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || payload.Data.Total != 4 || payload.Data.Succeeded != 1 || payload.Data.Failed != 3 {
		t.Fatal(w.Body.String())
	}
	if _, _, err := f.s.store.MailAccountCredential(f.email); err == nil {
		t.Fatal("selected account remained")
	}
	for _, email := range []string{"busy@example.com", "keep@example.com", "mail@example.com"} {
		if _, _, err := f.s.store.MailAccountCredential(email); err != nil {
			t.Fatal("unselected/protected account deleted", email)
		}
	}
	for _, item := range payload.Data.Items[1:] {
		if item.Error == "" || item.Deleted {
			t.Fatal("missing failure detail", item)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("deletion called OpenAI")
	}
}

func TestProDeleteProtectsJobsAndValidatesSelection(t *testing.T) {
	for _, mode := range []string{"automatic", "login", "oauth", "merge", "manual", "waiting"} {
		t.Run(mode, func(t *testing.T) {
			f := newProExecutionFixture(t)
			switch mode {
			case "automatic":
				f.s.proAutoJobs = map[string]context.CancelFunc{f.email: func() {}}
			case "login":
				f.s.registrationJobs = map[string]map[string]any{"job": {"email": f.email, "status": "running"}}
			case "oauth":
				f.s.oauthJobs = map[string]map[string]any{"job": {"email": f.email, "status": "queued"}}
			case "merge":
				f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProWorkflowRunning = true })
			case "manual":
				f.s.store.StartProManualStage(f.email, "push", "job")
			case "waiting":
				f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
					next := time.Now()
					p.ProAuto = model.ProAutoState{ID: "idle", Status: "waiting_quota", NextCheckAt: &next}
				})
			}
			err := f.s.deleteProAccount(f.email)
			if mode == "waiting" {
				if err != nil {
					t.Fatal(err)
				}
				due, err := f.s.store.DueProAutomations(time.Now().Add(time.Hour))
				if err != nil || len(due) != 0 {
					t.Fatal("deleted account remained scheduled")
				}
			} else if err == nil {
				t.Fatal("deleted active account")
			}
		})
	}
	f := newProExecutionFixture(t)
	for _, body := range []string{`{}`, `{"emails":[]}`, `{"emails":["invalid"]}`} {
		w := httptest.NewRecorder()
		f.s.deleteProAccounts(w, httptest.NewRequest("POST", "/api/pro-accounts/delete", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal("invalid selection accepted")
		}
	}
}

func TestProConcurrentDeleteIsSerialized(t *testing.T) {
	f := newProExecutionFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.s.deleteProAccount(f.email) }()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("deletion succeeded %d times", succeeded)
	}
}
