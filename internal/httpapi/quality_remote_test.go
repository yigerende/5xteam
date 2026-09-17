package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func qualityRemoteExample(p model.FreeAccountProfile) (sub2.RemoteQualitySnapshot, sub2.RemoteQualityResult) {
	now := time.Now().UTC()
	v := sub2.RemoteQualityVerdict{Status: "degraded", Degraded: true, Failures: 2, CheckedAt: &now, EvidenceAt: &now, StreakStartedAt: &now}
	r := sub2.RemoteQualityResult{AccountID: p.Sub2AccountID, Revision: "r1", Version: "v1", Question: sub2.RemoteQualityQuestion{RemoteQualityVerdict: v}, Model: sub2.RemoteQualityModel{RemoteQualityVerdict: v}}
	s := sub2.RemoteQualitySnapshot{Version: 1, ServerNow: now, Accounts: []sub2.RemoteQualityResult{r}}
	s.Settings.Enabled = true
	s.Settings.QuestionEnabled = true
	s.Settings.ModelAuditEnabled = true
	s.Settings.Revision = "r1"
	s.Settings.FailureLimit = 2
	s.Settings.RecoveryLimit = 2
	return s, r
}
func TestRemoteQualityConditionsFreshnessAndCycle(t *testing.T) {
	p := model.FreeAccountProfile{CreatedAt: time.Now().Add(-time.Hour)}
	for _, name := range []string{"any", "all", "stale", "prior-cycle", "prior-streak", "unknown", "disabled", "revision", "missing-version", "future", "error", "normal", "partial-all"} {
		t.Run(name, func(t *testing.T) {
			q := model.DefaultQualitySettings()
			q.ModelAuditEnabled = true
			q.ConditionMode = "any"
			s, r := qualityRemoteExample(p)
			child := p
			want := true
			switch name {
			case "all":
				q.ConditionMode = "all"
			case "stale":
				old := s.ServerNow.Add(-time.Hour)
				r.Question.EvidenceAt = &old
				r.Model.EvidenceAt = &old
				want = false
			case "prior-cycle":
				joined := s.ServerNow.Add(time.Second)
				child.PushedAt = &joined
				want = false
			case "prior-streak":
				old := p.CreatedAt.Add(-time.Minute)
				r.Question.StreakStartedAt = &old
				r.Model.StreakStartedAt = &old
				want = false
			case "unknown":
				r.Question.Status = "no_samples"
				r.Model.Status = "unknown"
				want = false
			case "disabled":
				s.Settings.Enabled = false
				want = false
			case "revision":
				r.Revision = "old"
				want = false
			case "missing-version":
				r.Version = ""
				want = false
			case "future":
				future := s.ServerNow.Add(time.Minute)
				r.Question.EvidenceAt = &future
				r.Model.EvidenceAt = &future
				want = false
			case "error":
				r.Question.Error = "timeout"
				r.Model.Error = "timeout"
				want = false
			case "normal":
				r.Question.Degraded = false
				r.Question.Status = "normal"
				r.Model.Degraded = false
				r.Model.Status = "normal"
				want = false
			case "partial-all":
				q.ConditionMode = "all"
				s.Settings.ModelAuditEnabled = false
				want = false
			}
			got, _ := remoteQualityDecision(q, child, s, r)
			if got != want {
				t.Fatalf("bad=%t want=%t", got, want)
			}
		})
	}
}
func TestRemoteQualityNoProbeDuplicateErrorAndRestore(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	s, r := qualityRemoteExample(p)
	f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", s, r, errors.New("offline"))
	if f.writes != 0 || f.read(t, p.ID).Quality.Failures != 0 {
		t.Fatal("read failure caused action")
	}
	f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", s, r, nil)
	if f.writes != 1 || !f.read(t, p.ID).Quality.Routed {
		t.Fatal("did not route confirmed result")
	}
	for i := 0; i < 5; i++ {
		f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", s, r, nil)
	}
	if f.writes != 1 || f.read(t, p.ID).Quality.Failures != 2 {
		t.Fatal("duplicate result counted or acted again")
	}
	f.q.AutoRestore = true
	f.save(t)
	r.Version = "v2"
	r.Question.Status = "normal"
	r.Question.Degraded = false
	r.Question.Failures = 0
	r.Question.Successes = 2
	f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", s, r, nil)
	if f.writes != 2 || f.read(t, p.ID).Quality.Routed {
		t.Fatal("restore failed")
	}
	r.Version = "v3"
	r.Question.Status = "degraded"
	r.Question.Degraded = true
	r.Question.Failures = 2
	r.Question.Successes = 0
	f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", s, r, nil)
	if f.writes != 3 {
		t.Fatal("new anomaly after recovery not handled")
	}
	if f.probes.Load() != 0 || len(f.auditRequests) != 0 {
		t.Fatal("space called test or usage APIs")
	}
}
func TestRemoteQualityConcurrentSameAccountAndFailedActionRetry(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	s, r := qualityRemoteExample(p)
	f.failWrite = true
	f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", s, r, nil)
	if f.read(t, p.ID).Quality.ActionStatus != "failed" {
		t.Fatal("missing failed action")
	}
	f.failWrite = false
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", s, r, nil)
		}()
	}
	wg.Wait()
	if f.writes != 2 || !f.read(t, p.ID).Quality.Routed {
		t.Fatalf("concurrent retry writes=%d", f.writes)
	}
}
func TestRemoteQualityReadBatchAndModelOnly(t *testing.T) {
	f := newQualityFixture(t)
	f.q.QuestionEnabled = false
	f.q.ModelAuditEnabled = true
	f.save(t)
	for i := int64(1); i <= 12; i++ {
		f.add(t, i)
	}
	f.s.runRemoteQualityBatch(context.Background(), true, "")
	if len(f.qualityReads) != 1 || len(f.qualityReads[0]) != 12 {
		t.Fatalf("not batched: %+v", f.qualityReads)
	}
	if f.probes.Load() != 0 || len(f.auditRequests) != 0 {
		t.Fatal("local detection still active")
	}
}
func TestRemoteQualityStaleResultCannotRetryDestructiveAction(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	s, r := qualityRemoteExample(p)
	f.failWrite = true
	f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", s, r, nil)
	f.failWrite = false
	s.ServerNow = s.ServerNow.Add(2 * time.Hour)
	f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", s, r, nil)
	if f.writes != 1 {
		t.Fatal("stale result retried destructive action")
	}
}

func TestRemoteQualitySameMotherSerialKicks(t *testing.T) {
	f := newQualityFixture(t)
	f.q.Action = "kick"
	f.q.Concurrency = 8
	f.save(t)
	first := f.add(t, 1)
	for i := int64(2); i <= 8; i++ {
		p := f.add(t, i)
		_, err := f.st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
			p.TeamAccountID, p.AdminAccountID = first.TeamAccountID, first.AdminAccountID
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	f.s.runRemoteQualityBatch(context.Background(), true, "")
	if f.kicks.Load() != 8 || f.kickMax.Load() != 1 {
		t.Fatalf("kicks=%d overlap=%d", f.kicks.Load(), f.kickMax.Load())
	}
	if f.probes.Load() != 0 {
		t.Fatal("unexpected answer probe")
	}
}

func TestRemoteQualityPolicyChangeCanKickPreviouslyRoutedAccount(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	snapshot, result := qualityRemoteExample(p)
	f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", snapshot, result, nil)
	if !f.read(t, p.ID).Quality.Routed {
		t.Fatal("routing failed")
	}
	f.q.Action = "kick"
	f.save(t)
	f.s.applyRemoteQuality(context.Background(), p, f.q, f.push, "password", snapshot, result, nil)
	if f.kicks.Load() != 1 {
		t.Fatal("new kick policy ignored for routed account")
	}
}
