package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
	"chatgpt-space-merge/internal/workflow"
)

type qualityFixture struct {
	remoteQuality *sub2.RemoteQualitySnapshot
	qualityReads  [][]int64
	s             *Server
	st            *store.Store
	push          model.Sub2Settings
	q             model.QualitySettings
	remote        *httptest.Server
	mu            sync.Mutex
	result        sub2.QualityProbeResult
	groups        map[int64][]int64
	requests      []map[string]any
	hook          func()
	failWrite     bool
	ambiguous     bool
	writes        int
	probes        atomic.Int32
	kicks         atomic.Int32
	active        atomic.Int32
	maxActive     atomic.Int32
	deletes       atomic.Int32
	failDelete    atomic.Bool
	kickStatus    atomic.Int32
	kickActive    atomic.Int32
	kickMax       atomic.Int32
	dir           string
	auditLogs     map[int64][]sub2.ModelAuditLog
	auditRequests [][]sub2.ModelAuditAccount
	auditStarts   []time.Time
	auditHook     func()
	auditStatus   int
}

func newQualityFixture(t *testing.T) *qualityFixture {
	t.Helper()
	f := &qualityFixture{groups: map[int64][]int64{}, result: sub2.QualityProbeResult{Complete: true, HTTPStatus: 200, Text: "FINAL_ANSWER=21", DurationMS: 100}}
	f.remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reply := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}) }
		if r.URL.Path == "/api/v1/admin/account-quality/capabilities" {
			reply(map[string]any{"version": 1, "max_accounts": 100, "read_only": true})
			return
		}
		if r.URL.Path == "/api/v1/admin/account-quality/results" {
			var input struct {
				AccountIDs []int64 `json:"account_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			f.mu.Lock()
			f.qualityReads = append(f.qualityReads, input.AccountIDs)
			var result sub2.RemoteQualitySnapshot
			if f.remoteQuality != nil {
				result = *f.remoteQuality
			} else {
				result.Version = 1
				result.ServerNow = time.Now().UTC()
				result.Settings.Enabled = true
				result.Settings.QuestionEnabled = true
				result.Settings.Revision = "remote-r1"
				result.Settings.FailureLimit = 2
				result.Settings.RecoveryLimit = 2
				for _, id := range input.AccountIDs {
					verdict := sub2.RemoteQualityVerdict{Status: "degraded", Degraded: true, Failures: 2, CheckedAt: &result.ServerNow, EvidenceAt: &result.ServerNow, StreakStartedAt: &result.ServerNow}
					result.Accounts = append(result.Accounts, sub2.RemoteQualityResult{AccountID: id, Revision: "remote-r1", Version: "remote-v1", Question: sub2.RemoteQualityQuestion{RemoteQualityVerdict: verdict}})
				}
			}
			hook := f.hook
			f.mu.Unlock()
			if hook != nil {
				hook()
			}
			reply(result)
			return
		}
		if r.URL.Path == "/api/v1/auth/login" {
			reply(map[string]any{"access_token": "test-admin"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "model-audit-capabilities") {
			reply(map[string]any{"version": 1, "max_accounts": 10, "logs_per_account": 3})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/usage/model-audit") {
			var input struct {
				Accounts []sub2.ModelAuditAccount `json:"accounts"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			f.mu.Lock()
			f.auditRequests = append(f.auditRequests, input.Accounts)
			f.auditStarts = append(f.auditStarts, time.Now())
			hook, status := f.auditHook, f.auditStatus
			results := []sub2.ModelAuditResult{}
			for _, a := range input.Accounts {
				results = append(results, sub2.ModelAuditResult{AccountID: a.AccountID, Logs: append([]sub2.ModelAuditLog{}, f.auditLogs[a.AccountID]...)})
			}
			f.mu.Unlock()
			if hook != nil {
				hook()
			}
			if status != 0 {
				w.WriteHeader(status)
				reply(nil)
				return
			}
			reply(map[string]any{"accounts": results})
			return
		}
		if r.URL.Path == "/api/v1/admin/groups/all" {
			reply([]map[string]any{{"id": 1, "name": "normal", "platform": "openai"}, {"id": 2, "name": "degraded", "platform": "openai"}, {"id": 9, "name": "other", "platform": "openai"}})
			return
		}
		if strings.Contains(r.URL.Path, "/users/") && r.Method == "DELETE" {
			f.kicks.Add(1)
			n := f.kickActive.Add(1)
			defer f.kickActive.Add(-1)
			for old := f.kickMax.Load(); n > old; old = f.kickMax.Load() {
				if f.kickMax.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			if code := f.kickStatus.Load(); code != 0 {
				w.WriteHeader(int(code))
				return
			}
			_, _ = w.Write([]byte(`{"message":"removed"}`))
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		var id int64
		if len(parts) > 5 {
			id, _ = strconv.ParseInt(parts[5], 10, 64)
		}
		if strings.HasSuffix(r.URL.Path, "/test") {
			if r.Header.Get("X-Quality-Global-Proxy") != "" {
				t.Error("manager must not override Sub2 proxy")
			}
			f.probes.Add(1)
			active := f.active.Add(1)
			defer f.active.Add(-1)
			for {
				old := f.maxActive.Load()
				if active <= old || f.maxActive.CompareAndSwap(old, active) {
					break
				}
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.requests = append(f.requests, body)
			result, hook := f.result, f.hook
			f.mu.Unlock()
			if hook != nil {
				hook()
			}
			w.Header().Set("Content-Type", "text/event-stream")
			event := func(v any) {
				data, _ := json.Marshal(v)
				fmt.Fprintf(w, "data: %s\n\n", data)
			}
			event(map[string]any{"type": "test_start", "model": body["model_id"], "data": map[string]any{"custom_prompt": true, "reasoning_effort": body["reasoning_effort"]}})
			if result.HTTPStatus != 200 {
				event(map[string]any{"type": "error", "error": fmt.Sprintf("API returned %d: test failure", result.HTTPStatus)})
			} else {
				event(map[string]any{"type": "content", "text": result.Text})
				if result.Complete {
					event(map[string]any{"type": "test_complete", "success": true})
				}
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/") && len(parts) == 6 {
			if r.Method == "DELETE" {
				f.deletes.Add(1)
				if f.failDelete.Load() {
					w.WriteHeader(502)
					reply(nil)
					return
				}
				reply(map[string]any{})
				return
			}
			if r.Method == "PUT" {
				var body struct {
					Groups []int64 `json:"group_ids"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.mu.Lock()
				f.writes++
				fail, amb := f.failWrite, f.ambiguous
				if !fail || amb {
					f.groups[id] = body.Groups
				}
				f.mu.Unlock()
				if fail {
					w.WriteHeader(502)
					reply(nil)
					return
				}
			}
			f.mu.Lock()
			groups := append([]int64{}, f.groups[id]...)
			f.mu.Unlock()
			reply(map[string]any{"id": id, "group_ids": groups, "status": "active", "schedulable": true})
			return
		}
		// Final cost refresh during a kick is advisory.
		if strings.HasSuffix(r.URL.Path, "/stats") {
			reply(map[string]any{})
			return
		}
		http.NotFound(w, r)
	}))
	var err error
	f.dir = t.TempDir()
	f.st, err = store.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.s, err = New(f.st, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.push = model.DefaultSub2Settings()
	f.push.URL = f.remote.URL
	f.push.Email = "admin@example.com"
	f.push.GroupIDs = []int64{1}
	f.push.Enable401Check = false
	f.push, err = f.st.SaveSub2Settings(f.push, "password")
	if err != nil {
		t.Fatal(err)
	}
	settings := model.DefaultSettings()
	settings.BaseURL = f.remote.URL
	if err = f.st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	auto := model.DefaultAutoRotationSettings()
	auto.TeamOperationIntervalSeconds = 0
	if _, err = f.st.SaveAutoRotationSettings(auto); err != nil {
		t.Fatal(err)
	}
	f.q = model.DefaultQualitySettings()
	f.q.Questions = nil
	f.q.Answer = "21"
	f.q.Concurrency = 2
	f.q.Enabled = true
	f.q.Action = "groups"
	f.q.NormalGroupIDs = []int64{1}
	f.q.DegradedGroupIDs = []int64{2}
	f.save(t)
	t.Cleanup(func() { f.s.Close(); f.st.Close(); f.remote.Close() })
	return f
}
func (f *qualityFixture) save(t *testing.T) {
	t.Helper()
	var err error
	f.q, err = f.st.SaveQualitySettings(f.q)
	if err != nil {
		t.Fatal(err)
	}
}
func (f *qualityFixture) add(t *testing.T, id int64) model.FreeAccountProfile {
	t.Helper()
	p, _, err := f.st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: fmt.Sprintf("quality-%d@example.com", id), UserID: fmt.Sprintf("user-%d", id)}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	admin := saveTestAdmin(t, f.st, model.AdminAccountProfile{Label: fmt.Sprintf("admin-%d", id), TeamAccountID: fmt.Sprintf("team-%d", id)}, "mother-token", f.remote.URL)
	p, err = f.st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
		p.AdminAccountID = admin.ID
		p.TeamAccountID = admin.TeamAccountID
		p.AcceptStatus = "completed"
		p.PushStatus = "completed"
		p.RemoveStatus = "pending"
		p.RemoveMethod = "child_leave"
		p.Sub2AccountID = id
		p.PushProvider = "sub2"
	})
	if err != nil {
		t.Fatal(err)
	}
	f.groups[id] = []int64{1, 9}
	return p
}
func (f *qualityFixture) read(t *testing.T, id string) model.FreeAccountProfile {
	t.Helper()
	p, _, err := f.st.FreeAccountCredential(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (f *qualityFixture) probe(p model.FreeAccountProfile) {
	f.s.probeQualityAccount(context.Background(), p, f.q, f.push, "password", true)
}

func TestQualityJudgingModesAndCustomAnswers(t *testing.T) {
	for _, tc := range []struct {
		mode, text string
		ms         int64
		pass       bool
	}{
		{"content", "FINAL_ANSWER=21", 25000, true}, {"content", "FINAL_ANSWER=20", 100, false},
		{"time", "wrong", 19999, true}, {"time", "21", 20000, false}, {"time", "21", 20001, false},
		{"content_time", "FINAL_ANSWER=21", 19999, true}, {"content_time", "FINAL_ANSWER=21", 20000, false}, {"content_time", "wrong", 100, false},
		{"content", "21", 1, true}, {"content", "The result is 21", 1, false}, {"content", "FINAL_ANSWER=21\nFINAL_ANSWER=20", 1, false},
	} {
		q := model.DefaultQualitySettings()
		q.Answer = "21"
		q.Mode = tc.mode
		got, _ := judgeQuality(q, tc.text, tc.ms)
		if got != tc.pass {
			t.Errorf("%+v got %v", tc, got)
		}
	}
	q := model.DefaultQualitySettings()
	q.Prompt = "What is the capital of France?"
	q.Answer = "Paris"
	if ok, _ := judgeQuality(q, "FINAL_ANSWER=Paris", 1); !ok {
		t.Fatal("custom answer not used")
	}
	q.MatchMode = "keyword"
	if ok, _ := judgeQuality(q, "Paris is the capital.", 1); !ok {
		t.Fatal("keyword")
	}
	q.MatchMode = "regex"
	q.Answer = `(?i)^paris[.!]?$ `
	q.Answer = strings.TrimSpace(q.Answer)
	if ok, _ := judgeQuality(q, "PARIS!", 1); !ok {
		t.Fatal("regex")
	}
}
func TestQualityConsecutiveResetErrorsAndRouting(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.result.Text = "FINAL_ANSWER=20"
	f.probe(p)
	if got := f.read(t, p.ID); got.Quality.Failures != 1 || got.Quality.Degraded || f.writes != 0 {
		t.Fatalf("first %+v", got.Quality)
	}
	f.result.Complete = false
	f.result.HTTPStatus = 429
	f.probe(p)
	if got := f.read(t, p.ID); got.Quality.Failures != 1 || got.Quality.Status != "error" || f.writes != 0 {
		t.Fatalf("429 %+v", got.Quality)
	}
	f.result.Complete = true
	f.result.HTTPStatus = 200
	f.result.Text = "FINAL_ANSWER=21"
	f.probe(p)
	if got := f.read(t, p.ID); got.Quality.Failures != 0 || got.Quality.Status != "normal" {
		t.Fatalf("reset %+v", got.Quality)
	}
	f.result.Text = "FINAL_ANSWER=20"
	f.probe(p)
	f.probe(p)
	got := f.read(t, p.ID)
	if !got.Quality.Routed || !got.Quality.Degraded || f.writes != 1 || !reflect.DeepEqual(f.groups[1], []int64{2, 9}) {
		t.Fatalf("route %+v %v", got.Quality, f.groups[1])
	}
	if got.RemoveStatus == "completed" || f.kicks.Load() != 0 || f.deletes.Load() != 0 {
		t.Fatal("routing deleted account")
	}
	f.probe(p)
	if f.writes != 1 {
		t.Fatal("duplicate routing")
	}
	f.q.AutoRestore = true
	f.save(t)
	f.result.Text = "FINAL_ANSWER=21"
	f.probe(p)
	if !f.read(t, p.ID).Quality.Routed {
		t.Fatal("restored too early")
	}
	f.probe(p)
	got = f.read(t, p.ID)
	if got.Quality.Degraded || got.Quality.Routed || !reflect.DeepEqual(f.groups[1], []int64{1, 9}) {
		t.Fatalf("restore %+v %v", got.Quality, f.groups[1])
	}
}
func TestQualityActionFailureAndAmbiguousWrite(t *testing.T) {
	for _, amb := range []bool{false, true} {
		t.Run(fmt.Sprint(amb), func(t *testing.T) {
			f := newQualityFixture(t)
			p := f.add(t, 1)
			f.result.Text = "wrong"
			f.failWrite = true
			f.ambiguous = amb
			f.probe(p)
			f.probe(p)
			got := f.read(t, p.ID)
			if amb {
				if !got.Quality.Routed {
					t.Fatal("committed write not verified")
				}
				return
			}
			if got.Quality.ActionStatus != "failed" || !got.Quality.PlanReady || f.kicks.Load() != 0 {
				t.Fatalf("failed %+v", got.Quality)
			}
			probes := f.probes.Load()
			f.failWrite = false
			f.probe(p)
			if f.probes.Load() != probes || !f.read(t, p.ID).Quality.Routed {
				t.Fatal("retry must only retry action")
			}
		})
	}
}
func TestQualityKickAndExclusion(t *testing.T) {
	f := newQualityFixture(t)
	f.q.Action = "kick"
	f.save(t)
	p := f.add(t, 1)
	f.result.Text = "wrong"
	f.probe(p)
	if f.kicks.Load() != 0 {
		t.Fatal("early kick")
	}
	f.probe(p)
	got := f.read(t, p.ID)
	if f.kicks.Load() != 1 || got.RemoveStatus != "completed" || !got.Quality.Excluded || got.Dead || got.RemoveMethod != "mother_kick" {
		t.Fatalf("kick %+v kicks=%d", got, f.kicks.Load())
	}
	if reusableAccount(got) || eligibleAutoRotationAccount(got) {
		t.Fatal("excluded selected")
	}
	f.probe(p)
	if f.kicks.Load() != 1 {
		t.Fatal("duplicate kick")
	}
}
func TestQualityIgnoresStaleAndChangedPolicy(t *testing.T) {
	for _, change := range []string{"removed", "config", "provider"} {
		t.Run(change, func(t *testing.T) {
			f := newQualityFixture(t)
			f.q.FailureLimit = 1
			f.save(t)
			p := f.add(t, 1)
			f.result.Text = "wrong"
			f.hook = func() {
				switch change {
				case "removed":
					_, _ = f.st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { now := time.Now(); p.RemoteRemovedAt = &now })
				case "config":
					q := f.q
					q.Answer = "changed"
					_, _ = f.st.SaveQualitySettings(q)
				case "provider":
					q := f.push
					q.Provider = "cpa"
					_, _ = f.st.SaveSub2Settings(q, "")
				}
			}
			f.probe(p)
			if got := f.read(t, p.ID); got.Quality.Failures != 0 || f.writes != 0 || f.kicks.Load() != 0 {
				t.Fatalf("stale %+v", got.Quality)
			}
		})
	}
}
func TestQualityConcurrencyEligibilityAndBusyAccount(t *testing.T) {
	f := newQualityFixture(t)
	for i := int64(1); i <= 12; i++ {
		f.add(t, i)
	}
	outside := f.add(t, 20)
	_, _ = f.st.UpdateFreeAccount(outside.ID, func(p *model.FreeAccountProfile) { p.AcceptStatus = "pending" })
	removed := f.add(t, 21)
	_, _ = f.st.UpdateFreeAccount(removed.ID, func(p *model.FreeAccountProfile) { p.RemoveStatus = "completed" })
	cpa := f.add(t, 22)
	_, _ = f.st.UpdateFreeAccount(cpa.ID, func(p *model.FreeAccountProfile) { p.PushProvider = "cpa" })
	busy := f.add(t, 23)
	unlock := f.s.lockFreeAccount(busy.ID)
	f.hook = func() { time.Sleep(35 * time.Millisecond) }
	f.s.runQualityBatch(context.Background(), true)
	unlock()
	if f.probes.Load() != 12 || f.maxActive.Load() != 2 {
		t.Fatalf("probes=%d max=%d", f.probes.Load(), f.maxActive.Load())
	}
	p := f.read(t, busy.ID)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f.probe(p) }()
	}
	wg.Wait()
	if f.probes.Load() != 13 {
		t.Fatalf("same account overlap=%d", f.probes.Load())
	}
	f.s.qualityRunMu.Lock()
	rec := httptest.NewRecorder()
	f.s.triggerQuality(rec, httptest.NewRequest("POST", "/api/quality/run", nil))
	f.s.qualityRunMu.Unlock()
	if rec.Code != 409 {
		t.Fatalf("overlap status=%d", rec.Code)
	}
}
func TestQualitySettingsPersistenceValidationAndPromptTransport(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.q.Prompt = "自定义问题：1+1？"
	f.q.Answer = "2"
	f.save(t)
	f.result.Text = "FINAL_ANSWER=2"
	f.probe(p)
	if got := f.read(t, p.ID); got.Quality.Status != "normal" {
		t.Fatalf("custom %+v", got.Quality)
	}
	if !strings.Contains(f.requests[0]["prompt"].(string), f.q.Prompt) || strings.Contains(f.requests[0]["prompt"].(string), "FINAL_ANSWER=2") {
		t.Fatal("prompt missing or expected answer leaked")
	}
	q, err := f.st.QualitySettings()
	if err != nil || q.Prompt != f.q.Prompt || q.Answer != "2" {
		t.Fatal("settings lost")
	}
	for _, mutate := range []func(*model.QualitySettings){func(q *model.QualitySettings) { q.Concurrency = 0 }, func(q *model.QualitySettings) { q.MatchMode = "regex"; q.Answer = "[" }, func(q *model.QualitySettings) { q.DegradedGroupIDs = []int64{1} }, func(q *model.QualitySettings) { q.IntervalSeconds = 0 }} {
		invalid := q
		mutate(&invalid)
		if _, err = f.st.SaveQualitySettings(invalid); err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
	if err = f.s.flushAuditEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := f.st.AutoRotationEventsByAccount(p.ID)
	found := false
	for _, e := range events {
		if e.Operation == "quality_result" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing quality audit")
	}
	count, next, err := f.st.QualitySchedule(f.q.Revision)
	if err != nil || count != 1 || next.Before(time.Now()) {
		t.Fatalf("countdown %d %v %v", count, next, err)
	}
}

func TestQualityKickFailureAndCleanupRetryAfterPolicyChange(t *testing.T) {
	for _, failure := range []string{"kick", "cleanup"} {
		t.Run(failure, func(t *testing.T) {
			f := newQualityFixture(t)
			f.q.Action, f.q.FailureLimit = "kick", 1
			f.save(t)
			p := f.add(t, 1)
			f.result.Text = "wrong"
			if failure == "kick" {
				f.kickStatus.Store(403)
			} else {
				f.failDelete.Store(true)
			}
			f.probe(p)
			got := f.read(t, p.ID)
			if got.Quality.ActionStatus != "failed" || !got.Quality.Excluded {
				t.Fatalf("%+v", got.Quality)
			}
			if failure == "cleanup" {
				f.q.Prompt += " Changed"
				f.save(t)
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/", nil)
				req.SetPathValue("id", p.ID)
				f.s.restoreQualityAccount(rec, req)
				if rec.Code != 409 {
					t.Fatal("must not restore before cleanup")
				}
			}
			f.kickStatus.Store(0)
			f.failDelete.Store(false)
			f.s.runQualityBatch(context.Background(), true)
			got = f.read(t, p.ID)
			if got.RemoveStatus != "completed" || !got.DownstreamCleaned || got.Quality.ActionStatus != "completed" {
				t.Fatalf("retry %+v", got)
			}
			want := int32(2)
			if failure == "cleanup" {
				want = 1
			}
			if f.kicks.Load() != want || f.probes.Load() != 1 {
				t.Fatalf("kick=%d probes=%d", f.kicks.Load(), f.probes.Load())
			}
		})
	}
}

func TestQualitySameMotherSerialKicks(t *testing.T) {
	f := newQualityFixture(t)
	f.q.Action, f.q.FailureLimit = "kick", 1
	f.save(t)
	first := f.add(t, 1)
	for i := int64(2); i <= 5; i++ {
		p := f.add(t, i)
		_, _ = f.st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
			p.TeamAccountID, p.AdminAccountID = first.TeamAccountID, first.AdminAccountID
		})
	}
	f.result.Text = "wrong"
	f.s.runQualityBatch(context.Background(), true)
	if f.kicks.Load() != 5 || f.kickMax.Load() != 1 {
		t.Fatalf("kicks=%d overlapping=%d", f.kicks.Load(), f.kickMax.Load())
	}
}

func TestQualityRestartRestoreAndHistoryLimit(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.result.Text = "wrong"
	f.probe(p)
	f.probe(p)
	f.s.Close()
	f.st.Close()
	var err error
	f.st, err = store.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.s, err = New(f.st, nil)
	if err != nil {
		t.Fatal(err)
	}
	q, err := f.st.QualitySettings()
	if err != nil || q.Revision != f.q.Revision || !f.read(t, p.ID).Quality.Routed {
		t.Fatal("restart lost durable state")
	}
	f.groups[1] = []int64{2, 9, 10}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("id", p.ID)
	f.s.restoreQualityAccount(rec, req)
	if rec.Code != 200 || !reflect.DeepEqual(f.groups[1], []int64{1, 9, 10}) || f.read(t, p.ID).Quality.Degraded {
		t.Fatalf("restore %d %s %v", rec.Code, rec.Body.String(), f.groups[1])
	}
	if err := f.s.flushAuditEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	history, err := f.st.QualityHistory(p.ID, 2)
	if err != nil || len(history) != 2 || history[0].CreatedAt.Before(history[1].CreatedAt) {
		t.Fatalf("history %+v %v", history, err)
	}
}

func TestQualityDisabledAndShutdownIgnoreInflightResult(t *testing.T) {
	for _, mode := range []string{"disabled", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			f := newQualityFixture(t)
			p := f.add(t, 1)
			f.q.FailureLimit = 1
			f.save(t)
			f.result.Text = "wrong"
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			f.hook = func() { once.Do(func() { close(entered) }); <-release }
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/", nil)
			f.s.triggerQuality(rec, req)
			if rec.Code != 202 {
				t.Fatalf("trigger %d", rec.Code)
			}
			<-entered
			if mode == "disabled" {
				f.q.Enabled = false
				f.save(t)
				close(release)
				f.s.qualityWG.Wait()
			} else {
				f.s.Close()
				close(release)
			}
			if got := f.read(t, p.ID); got.Quality.Failures != 0 || got.Quality.Degraded || f.writes != 0 {
				t.Fatalf("stale applied %+v", got.Quality)
			}
		})
	}
}

func TestQualityErrorsNeverCountAsWrongAnswers(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.result.Text = "wrong"
	f.probe(p)
	for _, status := range []int{0, 401, 403, 429, 500, 200} {
		f.result.HTTPStatus = status
		f.result.Complete = false
		f.probe(p)
		if got := f.read(t, p.ID); got.Quality.Failures != 1 || got.Quality.Degraded || got.Quality.Status != "error" {
			t.Fatalf("HTTP %d %+v", status, got.Quality)
		}
	}
	if f.writes != 0 || f.kicks.Load() != 0 {
		t.Fatal("transport error caused mutation")
	}
}

func TestQualityDefinitive401UsesExistingPolicy(t *testing.T) {
	f := newQualityFixture(t)
	f.push.Enable401Check = true
	f.push.ReloginFailureLimit = 0
	var err error
	f.push, err = f.st.SaveSub2Settings(f.push, "password")
	if err != nil {
		t.Fatal(err)
	}
	p := f.add(t, 1)
	f.result.Complete, f.result.HTTPStatus = false, 401
	f.probe(p)
	got := f.read(t, p.ID)
	if got.Quality.Degraded || got.Quality.Failures != 0 || !got.ReloginExhausted || got.RemoveStatus != "completed" || f.kicks.Load() != 1 {
		t.Fatalf("401 path %+v", got)
	}
}

func TestQualityGroupBindingChangeFailsWithoutMutation(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.result.Text = "wrong"
	f.failWrite = true
	f.probe(p)
	f.probe(p)
	writes := f.writes
	_, err := f.st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) { p.Sub2AccountID = 2 })
	if err != nil {
		t.Fatal(err)
	}
	f.probe(p)
	got := f.read(t, p.ID)
	if f.writes != writes || got.Quality.ActionStatus != "failed" || !strings.Contains(got.Quality.Error, "已改变") || got.Quality.NextAt == nil || got.Quality.NextAt.Before(time.Now()) {
		t.Fatalf("stale groups %+v", got.Quality)
	}
}

func TestQualitySummaryUsesLocalCurrentResults(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.probe(p)
	p = f.add(t, 2)
	f.result.Text = "wrong"
	f.probe(p)
	f.probe(p)
	p = f.add(t, 3)
	f.result.HTTPStatus = 429
	f.result.Complete = false
	f.probe(p)
	p = f.add(t, 4)
	f.result.HTTPStatus = 200
	f.result.Complete = true
	f.probe(p)
	p = f.add(t, 5)
	_, _ = f.st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
		now := time.Now()
		p.RemoteRemovedAt = &now
		p.RemoveStatus = "completed"
		p.Quality = model.AccountQuality{Action: "kick", ActionStatus: "completed", Excluded: true}
	})
	summary, err := f.st.QualitySummary(f.q.Revision)
	if err != nil || summary.Total != 4 || summary.Normal != 1 || summary.Degraded != 1 || summary.Errors != 1 || summary.Suspect != 1 || summary.Routed != 1 || summary.Processed != 2 || summary.Kicked != 1 || summary.ContentPassed != 1 || summary.TimePassed != 3 || summary.LastCheckedAt == nil {
		t.Fatalf("summary %+v %v", summary, err)
	}
	probes, writes := f.probes.Load(), f.writes
	for i := 0; i < 5; i++ {
		_ = f.s.qualityStatus()
	}
	if f.probes.Load() != probes || f.writes != writes {
		t.Fatal("status polling requested upstream")
	}
	f.q.Prompt += " New rule"
	f.save(t)
	summary, err = f.st.QualitySummary(f.q.Revision)
	if err != nil || summary.Normal != 0 || summary.ContentPassed != 0 || summary.Degraded != 1 || summary.Kicked != 1 {
		t.Fatalf("stale rule %+v %v", summary, err)
	}
}

// Opt-in browser fixture uses a temporary database and local-only fake upstream.
func TestQualityBrowserHarness(t *testing.T) {
	addr := os.Getenv("QUALITY_BROWSER_ADDR")
	if addr == "" {
		t.Skip("browser harness disabled")
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatal("loopback only")
	}
	f := newQualityFixture(t)
	f.s.jobs = workflow.NewManager(f.st)
	f.s.static = os.DirFS("../../webui/static")
	p := f.add(t, 1)
	f.probe(p)
	p = f.add(t, 2)
	f.result.Text = "wrong"
	f.probe(p)
	f.result.Text = "FINAL_ANSWER=21"
	f.q = model.DefaultQualitySettings()
	f.save(t)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("POST /__quality-test/stop", func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(stop) }) })
	mux.Handle("/", f.s.Handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(listener)
	t.Log("browser fixture at http://" + addr)
	select {
	case <-stop:
	case <-time.After(30 * time.Minute):
	}
	_ = srv.Close()
}
