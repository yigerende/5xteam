package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

type proQualityFixture struct {
	*proExecutionFixture
	mu              sync.Mutex
	mode            string
	groups          map[int64][]int64
	reads           [][]int64
	writes          int
	ambiguous       bool
	failGroupRead   bool
	probeAt         *time.Time
	connectionMode  string
	connectionTests int
	connectionHook  func()
	hook            func()
	schedulable     bool
	pauseCalls      int
	pauseMode       string
	pauseHook       func()
	events          []string
	cfg             model.ProSettings
	policy          model.ProQualitySettings
}

func newProQualityFixture(t *testing.T, action string) *proQualityFixture {
	t.Helper()
	f := &proQualityFixture{proExecutionFixture: newProExecutionFixture(t), mode: "bad", schedulable: true, groups: map[int64][]int64{42: {1, 9}}}
	// Drive polling explicitly; only merge jobs remain asynchronous.
	f.s.stopProQuality()
	f.s.stopProAutomations()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(data any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
		}
		switch r.URL.Path {
		case "/api/v1/auth/login":
			reply(map[string]string{"access_token": "pro-admin"})
			return
		case "/api/v1/admin/account-quality/capabilities":
			reply(map[string]any{"version": 1, "max_accounts": 100, "read_only": true})
			return
		case "/api/v1/admin/groups/all":
			reply([]map[string]any{{"id": 1, "name": "normal", "platform": "openai"}, {"id": 2, "name": "degraded", "platform": "openai"}, {"id": 9, "name": "unrelated", "platform": "openai"}})
			return
		case "/api/v1/admin/account-quality/results":
			var input struct {
				IDs []int64 `json:"account_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			f.mu.Lock()
			f.reads = append(f.reads, input.IDs)
			mode, hook, probeAt := f.mode, f.hook, f.probeAt
			f.mu.Unlock()
			if hook != nil {
				hook()
			}
			if mode == "error" {
				http.Error(w, "upstream unavailable", 503)
				return
			}
			snapshot := sub2.RemoteQualitySnapshot{Version: 1, ServerNow: time.Now().UTC()}
			snapshot.Settings.Enabled, snapshot.Settings.QuestionEnabled = true, true
			snapshot.Settings.Revision, snapshot.Settings.FailureLimit, snapshot.Settings.RecoveryLimit = "remote-r1", 2, 2
			if mode == "disabled" {
				snapshot.Settings.Enabled = false
			}
			if mode == "clock" {
				snapshot.ServerNow = snapshot.ServerNow.Add(time.Hour)
			}
			for _, id := range input.IDs {
				now := time.Now().UTC()
				if probeAt != nil {
					now = *probeAt
				}
				verdict := sub2.RemoteQualityVerdict{Status: "degraded", Degraded: true, Failures: 2, CheckedAt: &now, EvidenceAt: &now, StreakStartedAt: &now}
				if mode == "probe_error" {
					verdict.Status, verdict.Error, verdict.Degraded, verdict.Failures = "error", "probe timed out", false, 0
					verdict.EvidenceAt = nil
				}
				if mode == "normal" {
					verdict.Status, verdict.Degraded, verdict.Failures, verdict.Successes = "normal", false, 0, 2
				}
				if mode == "stale" {
					old := now.Add(-time.Hour)
					verdict.EvidenceAt = &old
					verdict.StreakStartedAt = &old
				}
				if mode == "old_streak" {
					old := now.Add(-24 * time.Hour)
					verdict.StreakStartedAt = &old
				}
				if mode == "unknown" {
					verdict.Status = "unknown"
					verdict.Degraded = false
					verdict.EvidenceAt = nil
				}
				v := sub2.RemoteQualityResult{AccountID: id, Revision: "remote-r1", Version: mode, Question: sub2.RemoteQualityQuestion{RemoteQualityVerdict: verdict, QuestionName: "fixture"}}
				if mode == "revision" {
					v.Revision = "old-r0"
				}
				snapshot.Accounts = append(snapshot.Accounts, v)
			}
			reply(snapshot)
			return
		}
		if r.URL.Path == "/api/v1/admin/accounts/42/test" && r.Method == "POST" {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["model_id"] != "gpt-6.1-sol" || body["mode"] != "default" || body["endpoint"] != "original" || body["prompt"] != "hi" {
				t.Error("connection recheck did not use the ordinary account test")
			}
			f.mu.Lock()
			f.connectionTests++
			mode, hook := f.connectionMode, f.connectionHook
			f.mu.Unlock()
			if hook != nil {
				hook()
			}
			if mode == "http_error" {
				http.Error(w, "unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"test_start\",\"model\":\"gpt-6.1-sol\",\"data\":{\"custom_prompt\":true}}\n\n")
			switch mode {
			case "dead":
				fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":\"API returned 401: account_deactivated / You do not have an account because it has been deleted or deactivated\"}\n\n")
			case "normal":
				fmt.Fprint(w, "data: {\"type\":\"content\",\"text\":\"Hello\"}\n\ndata: {\"type\":\"test_complete\",\"success\":true}\n\n")
			case "truncated":
				fmt.Fprint(w, "data: {\"type\":\"content\",\"text\":\"Hello\"}\n\n")
			case "empty":
				fmt.Fprint(w, "data: {\"type\":\"test_complete\",\"success\":true}\n\n")
			default:
				fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":\"API returned 401: unauthorized\"}\n\n")
			}
			return
		}
		const prefix = "/api/v1/admin/accounts/"
		if r.URL.Path == prefix+"42/schedulable" && r.Method == "POST" {
			var input struct {
				Schedulable *bool `json:"schedulable"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.Schedulable == nil || *input.Schedulable {
				t.Error("expected account scheduling pause only")
			}
			f.mu.Lock()
			f.pauseCalls++
			f.events = append(f.events, "pause")
			mode, hook := f.pauseMode, f.pauseHook
			if mode != "fail" && mode != "ignored" {
				f.schedulable = false
			}
			f.mu.Unlock()
			if hook != nil {
				hook()
			}
			if mode == "fail" || mode == "ambiguous" {
				http.Error(w, "pause response unavailable", 503)
			} else {
				reply(map[string]any{"id": 42})
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, prefix) {
			id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, prefix), 10, 64)
			f.mu.Lock()
			defer f.mu.Unlock()
			if r.Method == "GET" {
				if f.failGroupRead {
					f.failGroupRead = false
					http.Error(w, "verification unavailable", 503)
					return
				}
				f.events = append(f.events, "read")
				if f.pauseMode == "missing" {
					reply(map[string]any{"id": id, "group_ids": f.groups[id]})
				} else {
					reply(map[string]any{"id": id, "group_ids": f.groups[id], "schedulable": f.schedulable})
				}
				return
			}
			if r.Method == "PUT" {
				var body struct {
					Groups []int64 `json:"group_ids"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.groups[id] = body.Groups
				f.writes++
				f.events = append(f.events, "groups")
				if f.ambiguous {
					f.ambiguous = false
					f.failGroupRead = true
					http.Error(w, "lost response", 503)
					return
				}
				reply(map[string]any{"id": id})
				return
			}
		}
		t.Errorf("unexpected upstream call: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", 500)
	}))
	t.Cleanup(func() { f.s.stopProMergeJobs(); remote.Close() })
	cfg, _, _, _ := f.s.store.ProSettings()
	cfg.Provider, cfg.Sub2.URL, cfg.Sub2.Email = "sub2", remote.URL, "pro@example.com"
	var err error
	f.cfg, err = f.s.store.SaveProSettings(cfg, "pro-secret", "")
	if err != nil {
		t.Fatal(err)
	}
	f.policy = model.DefaultProQualitySettings()
	f.policy.Enabled, f.policy.Action, f.policy.Downstream = true, action, model.ProDownstream(f.cfg)
	f.policy.NormalGroupIDs, f.policy.DegradedGroupIDs, f.policy.AutoRestore = []int64{1}, []int64{2}, true
	f.policy, err = f.s.store.SaveProQualitySettings(f.policy)
	if err != nil {
		t.Fatal(err)
	}
	f.seed(t, f.email, 42)
	return f
}

func (f *proQualityFixture) seed(t *testing.T, email string, id int64) {
	t.Helper()
	if email != f.email {
		if _, err := f.s.store.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.store.UpdateMailAccountManagementScope(email, "pro"); err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.PushProvider, p.PushStatus, p.Sub2AccountID = "sub2", "completed", id
		ref := model.ProDownstream(f.cfg)
		p.ProPushDownstream = &ref
		past := time.Now().Add(-time.Minute)
		p.ProPushStartedAt = &past
	})
	if err != nil {
		t.Fatal(err)
	}
}
func (f *proQualityFixture) run(t *testing.T) {
	t.Helper()
	if err := f.s.runProQualityBatch(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}
func (f *proQualityFixture) profile(t *testing.T) model.MailAccountProfile {
	t.Helper()
	p, _, err := f.s.store.MailAccountCredential(f.email)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (f *proQualityFixture) setMode(mode string) { f.mu.Lock(); f.mode = mode; f.mu.Unlock() }

func TestProQualityGroupsRestoreAndAmbiguousWrite(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprint(ambiguous), func(t *testing.T) {
			f := newProQualityFixture(t, "groups")
			f.ambiguous = ambiguous
			f.run(t)
			if ambiguous {
				p := f.profile(t)
				if !p.ProQuality.PlanReady || p.ProQuality.ActionStatus != "failed" {
					t.Fatalf("restore baseline lost: %+v", p.ProQuality)
				}
				f.run(t)
			}
			p := f.profile(t)
			if !p.ProQuality.Routed || p.ProQuality.Status != "degraded" || !reflect.DeepEqual(f.groups[42], []int64{2, 9}) {
				t.Fatalf("bad route: %+v groups=%v", p.ProQuality, f.groups[42])
			}
			writes := f.writes
			f.run(t)
			if f.writes != writes {
				t.Fatal("same verdict repeated group write")
			}
			f.setMode("normal")
			f.run(t)
			p = f.profile(t)
			if p.ProQuality.Routed || p.ProQuality.Status != "normal" || p.ProQuality.PlanReady || !reflect.DeepEqual(f.groups[42], []int64{1, 9}) {
				t.Fatalf("bad restore: %+v groups=%v", p.ProQuality, f.groups[42])
			}
			team, _ := f.s.store.QualitySettings()
			if team.Enabled {
				t.Fatal("Pro changed Team policy")
			}
		})
	}
}

func TestProQualityMergeSkipsQuotaAndRunsFourSteps(t *testing.T) {
	f := newProQualityFixture(t, "merge")
	next := time.Now().Add(time.Hour)
	_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.Quota7D = &model.FreeQuotaWindow{UsedPercent: 1}
		p.ProAuto = model.ProAutoState{ID: "auto", Status: "waiting_quota", NextCheckAt: &next, Steps: map[string]string{"oauth": "completed", "push": "completed", "quota": "waiting"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	f.run(t)
	f.s.proMergeWG.Wait()
	p := f.profile(t)
	if p.ProQuality.ActionStatus != "completed" || p.ProAuto.Status != "completed" || p.ProAuto.Steps["quota"] != "skipped" || p.ProMergeTrigger != "quality" {
		t.Fatalf("merge did not finish: %+v / %+v / %s", p.ProQuality, p.ProAuto, p.ProLastError)
	}
	for _, stage := range []string{"invite", "accept", "transfer", "remove"} {
		if f.calls[stage] != 1 {
			t.Fatalf("%s called %d times", stage, f.calls[stage])
		}
	}
	f.run(t)
	if len(f.reads) != 2 {
		t.Fatalf("removed account queried again: %v", f.reads)
	}
}

func TestProQualityInvalidResultsNeverAct(t *testing.T) {
	for _, mode := range []string{"stale", "unknown", "disabled", "old_streak", "revision", "error", "clock"} {
		t.Run(mode, func(t *testing.T) {
			f := newProQualityFixture(t, "merge")
			f.setMode(mode)
			_ = f.s.runProQualityBatch(context.Background(), true)
			f.s.proMergeWG.Wait()
			p := f.profile(t)
			if p.ProQuality.MergeAttempts != 0 || len(f.calls) != 0 || p.ProQuality.SourceFresh {
				t.Fatalf("acted on %s: %+v", mode, p.ProQuality)
			}
		})
	}
}

func TestProQualityStoppedOrChangedAccountsNeverAct(t *testing.T) {
	for _, mode := range []string{"stopped", "paused", "unknown_transfer", "removed", "wrong_connection", "unbound", "cpa", "policy_changed", "binding_changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newProQualityFixture(t, "groups")
			change := func() {
				_, _ = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
					switch mode {
					case "stopped":
						p.ProAuto.StopRequested = true
					case "paused":
						p.ProMigration = &model.ProMigrationState{Paused: true, Downstream: model.ProDownstream(f.cfg)}
					case "unknown_transfer":
						p.TransferStatus = "unknown"
					case "removed":
						p.RemoveStatus = "completed"
					case "wrong_connection":
						p.ProPushDownstream = &model.ProDownstreamRef{Provider: "sub2", URL: "https://other.invalid"}
					case "unbound":
						p.ProPushDownstream = nil
					case "cpa":
						p.PushProvider = "cpa"
					case "binding_changed":
						p.Sub2AccountID = 99
					}
				})
			}
			if mode == "binding_changed" {
				f.hook = change
			} else if mode == "policy_changed" {
				f.hook = func() { q := f.policy; q.Enabled = false; _, _ = f.s.store.SaveProQualitySettings(q) }
			} else {
				change()
			}
			f.run(t)
			if f.writes != 0 {
				t.Fatalf("changed groups for %s", mode)
			}
		})
	}
}

func TestProQualityQueuedMergeRechecksRecoveryAndPolicy(t *testing.T) {
	for _, mode := range []string{"normal", "disabled", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			f := newProQualityFixture(t, "merge")
			value, _ := f.s.autoAdminLocks.LoadOrStore(f.admin.TeamAccountID, &sync.Mutex{})
			lock := value.(*sync.Mutex)
			lock.Lock()
			f.run(t)
			if mode == "normal" {
				f.setMode("normal")
			} else if mode == "disabled" {
				q := f.policy
				q.Enabled = false
				_, _ = f.s.store.SaveProQualitySettings(q)
			} else {
				_, _ = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = true })
			}
			lock.Unlock()
			f.s.proMergeWG.Wait()
			if len(f.calls) != 0 || f.profile(t).ProQuality.ActionStatus != "failed" {
				t.Fatal("queued stale action executed")
			}
		})
	}
}

func TestProQualityMergeRetryBoundAndUnknownTransfer(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			f := newProQualityFixture(t, "merge")
			if uncertain {
				f.loseTransferResponse = true
			} else {
				f.failStage, f.failCount = "invite", 99
			}
			f.run(t)
			f.s.proMergeWG.Wait()
			before := len(f.reads)
			f.run(t)
			f.s.proMergeWG.Wait()
			p := f.profile(t)
			if p.ProQuality.MergeAttempts != 1 || len(f.reads) != before+1 || f.calls["invite"] != 1 {
				t.Fatalf("unbounded retry: %+v %v", p.ProQuality, f.calls)
			}
			if uncertain && p.TransferStatus != "unknown" {
				t.Fatal("lost transfer response not guarded")
			}
		})
	}
}

func TestProQualityBatchingAndPolling(t *testing.T) {
	f := newProQualityFixture(t, "groups")
	f.setMode("normal")
	for i := 0; i < 104; i++ {
		f.seed(t, fmt.Sprintf("quality-%03d@example.com", i), int64(i+100))
	}
	f.run(t)
	if len(f.reads) != 2 || len(f.reads[0]) != 100 || len(f.reads[1]) != 5 {
		t.Fatalf("batching %v", f.reads)
	}
	if err := f.s.runProQualityBatch(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(f.reads) != 2 {
		t.Fatal("ignored query interval")
	}
}

func TestProQualitySettingsAndDisplay(t *testing.T) {
	f := newProQualityFixture(t, "groups")
	q := f.policy
	q.NormalGroupIDs = []int64{2}
	body, _ := json.Marshal(q)
	w := f.request(f.s.saveProQualitySettings, "PUT", "/api/pro-quality/settings", string(body))
	if w.Code != 400 {
		t.Fatalf("overlap accepted: %d", w.Code)
	}
	q = f.policy
	q.Action = "kick"
	body, _ = json.Marshal(q)
	w = f.request(f.s.saveProQualitySettings, "PUT", "/api/pro-quality/settings", string(body))
	if w.Code != 400 {
		t.Fatal("Team kick accepted")
	}
	q = f.policy
	q.DegradedGroupIDs = []int64{404}
	body, _ = json.Marshal(q)
	w = f.request(f.s.saveProQualitySettings, "PUT", "/api/pro-quality/settings", string(body))
	if w.Code != 400 {
		t.Fatal("nonexistent group accepted")
	}
	q = f.policy
	body, _ = json.Marshal(q)
	w = f.request(f.s.saveProQualitySettings, "PUT", "/api/pro-quality/settings", string(body))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f.policy, _ = f.s.store.ProQualitySettings()
	f.run(t)
	p := f.profile(t)
	expired := time.Now().Add(-time.Second)
	p.ProQuality.ValidUntil = &expired
	if proQualityDisplay(p, f.policy, f.cfg).Status != "stale" {
		t.Fatal("stale displayed as current")
	}
	cfg := f.cfg
	cfg.Sub2.URL = "https://different.invalid"
	if proQualityDisplay(p, f.policy, cfg).Status != "unknown" {
		t.Fatal("old binding displayed as current")
	}
	reads := len(f.reads)
	w = f.request(f.s.listProAccounts, "GET", "/api/pro-accounts?page=1&page_size=10", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"pro_quality"`) || len(f.reads) != reads {
		t.Fatal("list queried upstream or omitted quality")
	}
}

func TestProQualityFailedRestoreCanRouteAgain(t *testing.T) {
	f := newProQualityFixture(t, "groups")
	f.run(t)
	f.mu.Lock()
	f.ambiguous = true
	f.mu.Unlock()
	f.setMode("normal")
	f.run(t)
	if f.profile(t).ProQuality.ActionStatus != "failed" {
		t.Fatal("expected uncertain restore")
	}
	f.setMode("bad")
	f.run(t)
	if !reflect.DeepEqual(f.groups[42], []int64{2, 9}) || f.profile(t).ProQuality.ActionStatus != "completed" {
		t.Fatal("account left in normal group after renewed degradation")
	}
}

func TestProQualityCanSwitchFromGroupsToMerge(t *testing.T) {
	f := newProQualityFixture(t, "groups")
	f.run(t)
	q := f.policy
	q.Action = "merge"
	var err error
	f.policy, err = f.s.store.SaveProQualitySettings(q)
	if err != nil {
		t.Fatal(err)
	}
	f.run(t)
	f.s.proMergeWG.Wait()
	if f.profile(t).ProQuality.Action != "merge" || f.profile(t).RemoveStatus != "completed" {
		t.Fatal("completed group action blocked new merge policy")
	}
}

func TestProQualityTokenRefreshKeepsBindingButRepushExpiresSamples(t *testing.T) {
	f := newProQualityFixture(t, "groups")
	f.run(t)
	before := f.profile(t)
	now := time.Now()
	p := before
	p.OAuthAuthorizedAt = &now
	if proQualityIdentity(before, f.cfg) != proQualityIdentity(p, f.cfg) {
		t.Fatal("token renewal changed quality identity")
	}
	p.ProQualitySince = &now
	if proQualityIdentity(before, f.cfg) == proQualityIdentity(p, f.cfg) {
		t.Fatal("repush reused an old detection identity")
	}
}
