package httpapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func qualityLog(id, account int64, at time.Time, mismatch bool) sub2.ModelAuditLog {
	response := "gpt-6-astra"
	if mismatch {
		response = "different-model"
	}
	return sub2.ModelAuditLog{ID: id, AccountID: account, CreatedAt: at, SentModel: "gpt-6-astra", ResponseModel: response, Mismatch: &mismatch}
}
func TestQualityQuestionBankAdvanceRetryAndReset(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.q.Questions = model.DefaultQualityQuestions()
	f.save(t)
	f.result.Text = "wrong"
	f.probe(p)
	state := f.read(t, p.ID).Quality
	if state.Failures != 1 || state.NextQuestionID != "clock" || state.QuestionID != "candy" {
		t.Fatalf("first %+v", state)
	}
	f.result.HTTPStatus = 429
	f.probe(p)
	state = f.read(t, p.ID).Quality
	if state.Failures != 1 || state.NextQuestionID != "clock" {
		t.Fatalf("error advanced %+v", state)
	}
	f.result.HTTPStatus = 200
	f.result.Text = "FINAL_ANSWER=7.5"
	f.probe(p)
	state = f.read(t, p.ID).Quality
	if state.Failures != 0 || state.NextQuestionID != "percent" || state.Degraded {
		t.Fatalf("pass %+v", state)
	}
	f.result.Text = "wrong"
	f.probe(p)
	f.probe(p)
	state = f.read(t, p.ID).Quality
	if !state.QuestionDegraded || !state.Routed || f.writes != 1 {
		t.Fatalf("threshold %+v", state)
	}
}

func TestQualityQuestionOnlyCallsSub2WithoutManagerProxy(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	settings := f.st.Settings()
	settings.ProxyURL = ""
	if err := f.st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	f.probe(p)
	if got := f.read(t, p.ID).Quality; got.QuestionStatus != "normal" || got.Failures != 0 || f.probes.Load() != 1 {
		t.Fatalf("Sub2 probe should not depend on manager proxy %+v", got)
	}
}
func TestQualityModelLatestThreeDedupAndIndependentCounters(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.q.ModelAuditEnabled = true
	f.save(t)
	base := time.Now().UTC()
	first := qualityLog(1, 1, base, true)
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", []sub2.ModelAuditLog{first}, nil)
	if got := f.read(t, p.ID).Quality; got.ModelAudit.Failures != 1 || got.Degraded {
		t.Fatalf("first %+v", got)
	}
	f.result.Text = "FINAL_ANSWER=21"
	f.probe(p)
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", []sub2.ModelAuditLog{first}, nil)
	if got := f.read(t, p.ID).Quality; got.ModelAudit.Failures != 1 || got.Degraded || !got.ModelAudit.NoNewSamples {
		t.Fatalf("duplicate %+v", got)
	}
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", nil, errors.New("network error"))
	if got := f.read(t, p.ID).Quality; got.ModelAudit.Failures != 1 || got.Degraded {
		t.Fatalf("error %+v", got)
	}
	logs := []sub2.ModelAuditLog{qualityLog(3, 1, base.Add(2*time.Millisecond), true), qualityLog(2, 1, base.Add(time.Millisecond), true), first}
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", logs, nil)
	got := f.read(t, p.ID).Quality
	if !got.ModelAudit.Degraded || !got.Routed || got.Failures != 0 || f.writes != 1 {
		t.Fatalf("threshold %+v", got)
	}
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", logs, nil)
	if got = f.read(t, p.ID).Quality; got.ModelAudit.Failures != 3 || f.writes != 1 {
		t.Fatalf("repeated %+v", got)
	}
}
func TestQualityModelRecoveryNeedsBothSignals(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.q.ModelAuditEnabled = true
	f.q.AutoRestore = true
	f.save(t)
	base := time.Now().UTC()
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", []sub2.ModelAuditLog{qualityLog(1, 1, base, true), qualityLog(2, 1, base.Add(time.Millisecond), true)}, nil)
	f.probe(p)
	f.probe(p)
	if !f.read(t, p.ID).Quality.Routed || f.writes != 1 {
		t.Fatal("question success restored unresolved model mismatch")
	}
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", []sub2.ModelAuditLog{qualityLog(3, 1, base.Add(2*time.Millisecond), false), qualityLog(4, 1, base.Add(3*time.Millisecond), false)}, nil)
	if got := f.read(t, p.ID).Quality; got.Routed || got.Degraded || f.writes != 2 {
		t.Fatalf("restore %+v", got)
	}
}

func TestQualityRestoreRetryCancelledByNewModelFailure(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.q.ModelAuditEnabled, f.q.AutoRestore = true, true
	f.save(t)
	p, err := f.st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
		p.Quality = model.AccountQuality{Degraded: true, Routed: true, Action: "restore", ActionStatus: "failed", QuestionStatus: "normal", Successes: 2, ModelAudit: model.ModelAuditState{Status: "suspect", Failures: 1}}
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.applyQualityAction(context.Background(), p, f.q, f.push, "password")
	if got := f.read(t, p.ID).Quality; !got.Routed || got.Action != "groups" || f.writes != 0 {
		t.Fatalf("stale restore executed: %+v", got)
	}
}
func TestQualityModelSamplesUnknownVariantOrderAndStale(t *testing.T) {
	q := model.DefaultQualitySettings()
	q.FailureLimit = 3
	a := model.ModelAuditState{}
	base := time.Now().UTC()
	applyModelSamples(&a, []sub2.ModelAuditLog{qualityLog(3, 1, base.Add(2*time.Second), true), qualityLog(1, 1, base, true), qualityLog(2, 1, base.Add(time.Second), false)}, q, base)
	if a.Failures != 1 || a.LatestID != 3 || a.Degraded {
		t.Fatalf("order %+v", a)
	}
	late := qualityLog(4, 1, base.Add(time.Millisecond), true)
	applyModelSamples(&a, []sub2.ModelAuditLog{late}, q, base)
	if a.Failures != 1 || a.LatestID != 3 {
		t.Fatal("late record changed newer state")
	}
	unknown := qualityLog(5, 1, base.Add(3*time.Second), true)
	unknown.ResponseModel = ""
	applyModelSamples(&a, []sub2.ModelAuditLog{unknown}, q, base)
	if a.Failures != 1 || a.Status != "unknown" {
		t.Fatal("missing model counted")
	}
	variant := qualityLog(6, 1, base.Add(4*time.Second), true)
	variant.ResponseModel = "gpt-6-astra-2026-09-17"
	applyModelSamples(&a, []sub2.ModelAuditLog{variant}, q, base)
	if a.Failures != 0 || a.Status != "variant" {
		t.Fatal("variant counted")
	}
}
func TestQualityModelBatchFiftyAccountsBoundedRequests(t *testing.T) {
	f := newQualityFixture(t)
	f.q.QuestionEnabled = false
	f.q.ModelAuditEnabled = true
	f.save(t)
	for i := int64(1); i <= 50; i++ {
		f.add(t, i)
	}
	f.s.runQualityModelBatch(context.Background(), false, "")
	if len(f.auditRequests) != 5 || f.probes.Load() != 0 {
		t.Fatalf("requests=%d probes=%d", len(f.auditRequests), f.probes.Load())
	}
	for i, batch := range f.auditRequests {
		if len(batch) != 10 {
			t.Fatalf("batch %d=%d", i, len(batch))
		}
		if i > 0 && f.auditStarts[i].Sub(f.auditStarts[i-1]) < 490*time.Millisecond {
			t.Fatal("batch rate exceeded")
		}
	}
	f.s.runQualityModelBatch(context.Background(), false, "")
	if len(f.auditRequests) != 5 {
		t.Fatal("schedule ignored")
	}
}
func TestQualityModelBusyAccountAndStaleConfiguration(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.q.ModelAuditEnabled = true
	f.save(t)
	logs := []sub2.ModelAuditLog{qualityLog(1, 1, time.Now().UTC(), true)}
	v, _ := f.s.freeLocks.LoadOrStore(p.ID, &sync.Mutex{})
	lock := v.(*sync.Mutex)
	lock.Lock()
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", logs, nil)
	lock.Unlock()
	if f.read(t, p.ID).Quality.ModelAudit.CheckedAt != nil {
		t.Fatal("busy account was modified")
	}
	old := f.q
	f.q.Enabled = false
	f.save(t)
	f.s.applyQualityModelResult(context.Background(), p, old, f.push, "password", logs, nil)
	if f.read(t, p.ID).Quality.ModelAudit.CheckedAt != nil {
		t.Fatal("stale policy was applied")
	}
}
func TestQualityModelRestartKeepsSeenLogs(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.q.ModelAuditEnabled = true
	f.save(t)
	logs := []sub2.ModelAuditLog{qualityLog(1, 1, time.Now().UTC(), true)}
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", logs, nil)
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
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", logs, nil)
	if got := f.read(t, p.ID).Quality.ModelAudit; got.Failures != 1 || !got.NoNewSamples {
		t.Fatalf("restart %+v", got)
	}
}
func TestQualityModelAndQuestionConcurrentActionOnce(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.q.ModelAuditEnabled = true
	f.q.FailureLimit = 1
	f.save(t)
	f.result.Text = "wrong"
	logs := []sub2.ModelAuditLog{qualityLog(1, 1, time.Now().UTC(), true)}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				f.probe(p)
			} else {
				f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", logs, nil)
			}
		}(i)
	}
	wg.Wait()
	if f.writes != 1 || !f.read(t, p.ID).Quality.Routed {
		t.Fatalf("writes=%d", f.writes)
	}
}

func TestQualityModelForcedKickAndCycleBoundary(t *testing.T) {
	f := newQualityFixture(t)
	p := f.add(t, 1)
	f.q.QuestionEnabled = false
	f.q.ModelAuditEnabled = true
	f.q.Action = "kick"
	f.save(t)
	now := time.Now().UTC()
	logs := []sub2.ModelAuditLog{qualityLog(1, 1, now.Add(-time.Hour), true), qualityLog(2, 1, now, true)}
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", logs, nil)
	if got := f.read(t, p.ID).Quality; got.ModelAudit.Failures != 1 || f.kicks.Load() != 0 {
		t.Fatalf("old cycle %+v", got)
	}
	logs = append(logs[1:], qualityLog(3, 1, now.Add(time.Millisecond), true))
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", logs, nil)
	got := f.read(t, p.ID)
	if f.kicks.Load() != 1 || got.RemoveStatus != "completed" || !got.Quality.Excluded || got.Dead {
		t.Fatalf("forced mother kick %+v", got)
	}
	f.s.applyQualityModelResult(context.Background(), p, f.q, f.push, "password", logs, nil)
	if f.kicks.Load() != 1 {
		t.Fatal("repeated kick")
	}
}

func TestQualityModelHundredAccountsAndInvalidResponse(t *testing.T) {
	f := newQualityFixture(t)
	f.q.QuestionEnabled = false
	f.q.ModelAuditEnabled = true
	f.save(t)
	mother := f.add(t, 1)
	for i := int64(2); i <= 100; i++ {
		p, _, err := f.st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: fmt.Sprintf("quality-%d@example.com", i), UserID: fmt.Sprintf("user-%d", i)}, "source-token")
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
			p.AdminAccountID = mother.AdminAccountID
			p.TeamAccountID = mother.TeamAccountID
			p.AcceptStatus = "completed"
			p.PushStatus = "completed"
			p.RemoveStatus = "pending"
			p.PushProvider = "sub2"
			p.Sub2AccountID = i
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	f.s.runQualityModelBatch(context.Background(), true, "")
	if len(f.auditRequests) != 10 {
		t.Fatalf("requests=%d", len(f.auditRequests))
	}
	accounts, _ := f.st.QualityAccounts()
	p := accounts[0]
	f.auditLogs = map[int64][]sub2.ModelAuditLog{p.Sub2AccountID: {qualityLog(99, p.Sub2AccountID+1000, time.Now().UTC(), true)}}
	f.s.runQualityModelBatch(context.Background(), true, p.ID)
	if got := f.read(t, p.ID).Quality; got.ModelAudit.Failures != 0 || got.ModelAudit.Status != "error" {
		t.Fatalf("mismatched identity %+v", got)
	}
}
