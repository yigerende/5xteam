package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

type proGroupFixture struct {
	*proExecutionFixture
	remote             *httptest.Server
	mu                 sync.Mutex
	groups             []int64
	reads, writes      atomic.Int32
	fail, lostResponse atomic.Bool
	entered, release   chan struct{}
}

func newProGroupFixture(t *testing.T) *proGroupFixture {
	t.Helper()
	f := &proGroupFixture{proExecutionFixture: newProExecutionFixture(t), groups: []int64{1}}
	f.remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/login" {
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture-token"}}`)
			return
		}
		if r.URL.Path != "/api/v1/admin/accounts/42" {
			t.Errorf("unexpected downstream request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		p, _, _ := f.s.store.MailAccountCredential(f.email)
		if p.TransferStatus != "completed" || p.RemoveStatus != "completed" || p.ProPostMergeGroups == nil || p.ProPostMergeGroups.Status != "running" {
			t.Error("groups requested before successful persisted removal or missing progress")
		}
		if r.Method == "GET" {
			f.reads.Add(1)
			f.mu.Lock()
			defer f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"id": 42, "group_ids": f.groups}})
			return
		}
		if r.Method != "PUT" {
			t.Errorf("unexpected method %s", r.Method)
			return
		}
		f.writes.Add(1)
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["group_ids"] == nil {
			t.Error("must update only group_ids")
		}
		if f.entered != nil {
			close(f.entered)
			select {
			case <-f.release:
			case <-r.Context().Done():
			}
		}
		if f.fail.Load() {
			w.WriteHeader(502)
			fmt.Fprint(w, `{"message":"fixture unavailable"}`)
			return
		}
		f.mu.Lock()
		_ = json.Unmarshal(body["group_ids"], &f.groups)
		f.mu.Unlock()
		if f.lostResponse.Load() {
			w.WriteHeader(502)
			fmt.Fprint(w, `{"message":"response lost after commit"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"id":42}}`)
	}))
	t.Cleanup(f.remote.Close)
	v, _, _, _ := f.s.store.ProSettings()
	v.Sub2.URL, v.Sub2.Email = f.remote.URL, "fixture@example.com"
	v.PostMergeGroupIDs, v.PostMergeGroupNames = []int64{7, 8}, []string{"A", "B"}
	if _, err := f.s.store.SaveProSettings(v, "password", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.PushProvider = "sub2" }); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestProPostMergeGroupsOnlyAfterRemoval(t *testing.T) {
	for _, name := range []string{"manual", "lost_response", "empty_selection", "cpa", "transfer_failed", "remove_failed", "historical_completed"} {
		t.Run(name, func(t *testing.T) {
			f := newProGroupFixture(t)
			switch name {
			case "empty_selection", "cpa":
				v, _, _, _ := f.s.store.ProSettings()
				if name == "cpa" {
					v.Provider = "cpa"
				} else {
					v.PostMergeGroupIDs = nil
				}
				if _, err := f.s.store.SaveProSettings(v, "", ""); err != nil {
					t.Fatal(err)
				}
			case "transfer_failed":
				f.failStage, f.failCount = "transfer", 9
			case "remove_failed":
				f.failStage, f.failCount = "remove", 9
			case "lost_response":
				f.lostResponse.Store(true)
			case "historical_completed":
				_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
					for _, stage := range []string{"invite", "accept", "transfer", "remove"} {
						setProStage(p, stage, "completed")
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			out := f.mergeAndWait(t)
			failed := name == "transfer_failed" || name == "remove_failed"
			if (out.Code != 200) != failed {
				t.Fatalf("merge result %d %s", out.Code, out.Body.String())
			}
			p, _, _ := f.s.store.MailAccountCredential(f.email)
			if name == "manual" || name == "lost_response" {
				if p.ProPostMergeGroups == nil || p.ProPostMergeGroups.Status != "completed" || f.writes.Load() != 1 || p.ProMergeProgress() != "completed" {
					t.Fatalf("missing completion: %+v writes=%d", p.ProPostMergeGroups, f.writes.Load())
				}
				f.mu.Lock()
				groups := slices.Clone(f.groups)
				f.mu.Unlock()
				if !slices.Equal(groups, []int64{7, 8}) {
					t.Fatalf("groups not replaced: %v", groups)
				}
				if p.Sub2AccountID != 42 || p.PushStatus != "completed" {
					t.Fatal("changed downstream identity")
				}
				reads := f.reads.Load()
				if next := f.mergeAndWait(t); next.Code != 200 {
					t.Fatal(next.Body.String())
				}
				if f.reads.Load() != reads || f.writes.Load() != 1 {
					t.Fatal("completed task ran twice")
				}
			} else if f.reads.Load() != 0 || f.writes.Load() != 0 {
				t.Fatal("unexpected downstream grouping")
			}
			if name == "remove_failed" && (p.ProPostMergeGroups == nil || p.ProPostMergeGroups.Status != "pending") {
				t.Fatal("removal snapshot not saved")
			}
		})
	}
}

func TestProPostMergeGroupsFailureRetryOnlyGroups(t *testing.T) {
	f := newProGroupFixture(t)
	f.fail.Store(true)
	if out := f.mergeAndWait(t); out.Code != 400 {
		t.Fatal(out.Code, out.Body.String())
	}
	p, creds, _ := f.s.store.MailAccountCredential(f.email)
	if p.RemoveStatus != "completed" || p.ProPostMergeGroups.Status != "failed" || p.ProMergeProgress() != "failed" || !strings.Contains(p.ProLastError, "分组调整失败") {
		t.Fatalf("failure erased successful removal: %+v", p)
	}
	// Credential updates and config changes cannot discard/rebind a pending task.
	if _, err := f.s.store.SaveMailAccount(model.MailAccountProfile{Email: f.email}, creds); err != nil {
		t.Fatal(err)
	}
	v, _, _, _ := f.s.store.ProSettings()
	v.TargetAdminID, v.PostMergeGroupIDs = "no-longer-exists", []int64{99}
	if _, err := f.s.store.SaveProSettings(v, "", ""); err != nil {
		t.Fatal(err)
	}
	f.fail.Store(false)
	if out := f.mergeAndWait(t); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	p, _, _ = f.s.store.MailAccountCredential(f.email)
	if p.ProPostMergeGroups.Status != "completed" || p.ProLastError != "" {
		t.Fatal("retry did not complete")
	}
	f.proExecutionFixture.mu.Lock()
	defer f.proExecutionFixture.mu.Unlock()
	for _, stage := range []string{"invite", "accept", "transfer", "remove"} {
		if f.calls[stage] != 1 {
			t.Fatalf("repeated OpenAI stage %s: %v", stage, f.calls)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.groups, []int64{7, 8}) {
		t.Fatal("retry changed snapshotted target", f.groups)
	}
}

func TestProPostMergeGroupsConcurrentClicks(t *testing.T) {
	f := newProGroupFixture(t)
	f.entered, f.release = make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(f.release) })
	if out := f.request(f.s.mergeProAccount, "POST", "/merge", ""); out.Code != 202 {
		t.Fatal(out.Body.String())
	}
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("group update not reached")
	}
	p, _, _ := f.s.store.MailAccountCredential(f.email)
	if p.ProPostMergeGroups.Status != "running" || p.RemoveStatus != "completed" || p.ProMergeProgress() != "running" {
		t.Fatal("wrong in-flight state")
	}
	value, _ := f.s.autoAdminLocks.Load(f.admin.TeamAccountID)
	if lock := value.(*sync.Mutex); lock.TryLock() {
		lock.Unlock()
	} else {
		t.Fatal("Sub2 request is blocking unrelated Team operations")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out := f.request(f.s.mergeProAccount, "POST", "/merge", ""); out.Code != 409 {
				t.Errorf("concurrent click accepted: %d", out.Code)
			}
		}()
	}
	wg.Wait()
	release.Do(func() { close(f.release) })
	f.waitMerge(t)
	if f.writes.Load() != 1 {
		t.Fatal("duplicate group write")
	}
}

func TestProPostMergeGroupsRestartAndIdentityGuard(t *testing.T) {
	for _, name := range []string{"restart_already_applied", "changed_server", "changed_login", "changed_account", "import_wrong_server"} {
		t.Run(name, func(t *testing.T) {
			f := newProGroupFixture(t)
			v, _, _, _ := f.s.store.ProSettings()
			p, _, _ := f.s.store.MailAccountCredential(f.email)
			if _, err := f.s.prepareProPostMergeGroups(p, v); err != nil {
				t.Fatal(err)
			}
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				for _, stage := range []string{"invite", "accept", "transfer", "remove"} {
					setProStage(p, stage, "completed")
				}
				p.ProPostMergeGroups.Status = "running"
				if name == "changed_account" {
					p.Sub2AccountID = 43
				}
				if name == "import_wrong_server" {
					p.ProMigration = &model.ProMigrationState{Downstream: model.ProDownstreamRef{Provider: "sub2", URL: "http://different.invalid"}}
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.store.RecoverProWorkflows(); err != nil {
				t.Fatal(err)
			}
			p, _, _ = f.s.store.MailAccountCredential(f.email)
			if p.ProPostMergeGroups.Status != "pending" {
				t.Fatal("restart left spinner running")
			}
			if name == "changed_server" {
				v.Sub2.URL += "/other"
			}
			if name == "changed_login" {
				v.Sub2.Email = "other@example.com"
			}
			if _, err = f.s.store.SaveProSettings(v, "", ""); err != nil {
				t.Fatal(err)
			}
			f.groups = []int64{8, 7}
			out := f.mergeAndWait(t)
			if name == "restart_already_applied" {
				if out.Code != 200 || f.reads.Load() != 1 {
					t.Fatal(out.Code, out.Body.String(), f.reads.Load())
				}
			} else if out.Code != 400 || f.reads.Load() != 0 {
				t.Fatal("identity guard failed", out.Code, out.Body.String())
			}
			if f.writes.Load() != 0 || len(f.calls) != 0 {
				t.Fatal("restart repeated successful operations")
			}
		})
	}
}

func TestProPostMergeGroupsAutomationCompletion(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f := newProGroupFixture(t)
			f.fail.Store(fail)
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				p.ProAuto = model.ProAutoState{ID: "fixture-auto", Status: "running", Stage: "merge", Steps: map[string]string{"push": "completed", "quota": "completed"}}
			})
			if err != nil {
				t.Fatal(err)
			}
			err = f.s.runProAutomationTail(t.Context(), f.email)
			if (err != nil) != fail {
				t.Fatal("wrong auto result", err)
			}
			p, _, _ := f.s.store.MailAccountCredential(f.email)
			want := "completed"
			if fail {
				want = "failed"
			}
			if p.ProAuto.Status != want || p.ProAuto.Steps["merge"] != want || p.RemoveStatus != "completed" {
				t.Fatalf("bad automation result %+v", p.ProAuto)
			}
		})
	}
}
