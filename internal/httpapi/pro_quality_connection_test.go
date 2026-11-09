package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func (f *proQualityFixture) setConnectionMode(mode string) {
	f.mu.Lock()
	f.connectionMode = mode
	f.mu.Unlock()
}

func TestProQualityConnectionStatusSettingsAPI(t *testing.T) {
	f := newProQualityFixture(t, "groups")
	q := f.policy
	q.ConnectionFailureHTTPStatuses = " 502, 504,502 "
	body, _ := json.Marshal(q)
	w := f.request(f.s.saveProQualitySettings, "PUT", "/api/pro-quality/settings", string(body))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"connection_failure_http_statuses":"502,504"`) {
		t.Fatalf("save response: %d %s", w.Code, w.Body.String())
	}
	w = f.request(f.s.getProQualitySettings, "GET", "/api/pro-quality/settings", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"connection_failure_http_statuses":"502,504"`) {
		t.Fatalf("get response: %d %s", w.Code, w.Body.String())
	}
	q.ConnectionFailureHTTPStatuses = "200,503"
	body, _ = json.Marshal(q)
	w = f.request(f.s.saveProQualitySettings, "PUT", "/api/pro-quality/settings", string(body))
	if w.Code != 400 {
		t.Fatalf("invalid statuses accepted: %d", w.Code)
	}
	saved, _ := f.s.store.ProQualitySettings()
	if saved.ConnectionFailureHTTPStatuses != "502,504" {
		t.Fatal("rejected save changed policy")
	}
	var legacy map[string]any
	_ = json.Unmarshal(body, &legacy)
	delete(legacy, "connection_failure_http_statuses")
	body, _ = json.Marshal(legacy)
	w = f.request(f.s.saveProQualitySettings, "PUT", "/api/pro-quality/settings", string(body))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"connection_failure_http_statuses":"429,502,503"`) {
		t.Fatalf("legacy client lost defaults: %d %s", w.Code, w.Body.String())
	}
}

func TestProQualityConnectionRecoveryPreventsEveryAction(t *testing.T) {
	for _, action := range []string{"groups", "merge", "siwc"} {
		for _, source := range []string{"error", "probe_error"} {
			t.Run(action+"/"+source, func(t *testing.T) {
				f := newProQualityFixture(t, action)
				f.setMode(source)
				f.setConnectionMode("normal")
				for i := 0; i < 2; i++ {
					_ = f.s.runProQualityBatch(context.Background(), true)
					if i == 0 && f.connectionTests != 0 {
						t.Fatal("tested before the configured failure limit")
					}
				}
				f.s.proMergeWG.Wait()
				p := f.profile(t)
				if f.connectionTests != 1 || p.ProQuality.Status != "normal" || p.ProQuality.ErrorFailures != 0 || p.ProQuality.ErrorDegraded || p.ProQuality.Degraded || p.ProQuality.Error != "" || !p.ProQuality.SourceRecovered {
					t.Fatalf("connection recovery was not saved: tests=%d state=%+v", f.connectionTests, p.ProQuality)
				}
				if len(f.calls) != 0 || f.writes != 0 || p.ProSIWC != nil || p.ProQuality.Action != "" {
					t.Fatalf("recovery still dispatched %s", action)
				}
			})
		}
	}
}

func TestProQualityConnectionConfiguredStatusesTriggerHandling(t *testing.T) {
	for _, mode := range []string{"upstream_429", "upstream_502", "upstream_503"} {
		t.Run(mode, func(t *testing.T) {
			f := newProQualityFixture(t, "groups")
			f.setMode("error")
			f.setConnectionMode(mode)
			_ = f.s.runProQualityBatch(context.Background(), true)
			_ = f.s.runProQualityBatch(context.Background(), true)
			state := f.profile(t).ProQuality
			if f.connectionTests != 1 || !state.ErrorDegraded || state.ActionStatus != "completed" || f.writes != 1 || state.ConnectionCheck == nil || state.ConnectionCheck.Status != "error" {
				t.Fatalf("failed connection did not preserve handling: %+v", state)
			}
		})
	}
}

func TestProQualityConnectionUnmatchedFailurePreventsEveryAction(t *testing.T) {
	for _, action := range []string{"groups", "merge", "siwc"} {
		for _, mode := range []string{"upstream_400", "upstream_401", "upstream_403", "upstream_500", "upstream_504", "http_error", "truncated", "empty", "upstream_0"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				f := newProQualityFixture(t, action)
				q := f.policy
				q.ErrorFailureLimit, q.PauseBeforeAction = 1, true
				if _, err := f.s.store.SaveProQualitySettings(q); err != nil {
					t.Fatal(err)
				}
				f.setMode("probe_error")
				f.setConnectionMode(mode)
				f.run(t)
				f.s.proMergeWG.Wait()
				p := f.profile(t)
				if f.connectionTests != 1 || p.ProQuality.Status != "error" || p.ProQuality.Degraded || p.ProQuality.ErrorDegraded || p.ProQuality.SourceRecovered || p.ProQuality.ValidUntil != nil || p.ProQuality.ConnectionCheck == nil || p.ProQuality.ConnectionCheck.Status != "unconfirmed" {
					t.Fatalf("unmatched error became an actionable result: %+v", p.ProQuality)
				}
				if len(f.calls) != 0 || f.writes != 0 || f.pauseCalls != 0 || p.ProSIWC != nil || p.ProQuality.ActionStarted {
					t.Fatalf("unmatched error dispatched %s", action)
				}
			})
		}
	}
}

func TestProQualityConnectionCustomStatusesAndNextPoll(t *testing.T) {
	f := newProQualityFixture(t, "groups")
	q := f.policy
	q.ErrorFailureLimit, q.ConnectionFailureHTTPStatuses = 1, " 504,502,504 "
	q, err := f.s.store.SaveProQualitySettings(q)
	if err != nil {
		t.Fatal(err)
	}
	if q.ConnectionFailureHTTPStatuses != "504,502" {
		t.Fatalf("not normalized: %+v", q)
	}
	f.setMode("probe_error")
	f.setConnectionMode("upstream_429")
	f.run(t)
	if f.writes != 0 || f.profile(t).ProQuality.Degraded {
		t.Fatal("removed default code still triggered")
	}
	f.setConnectionMode("upstream_504")
	f.run(t)
	if f.writes != 1 || f.connectionTests != 2 || !f.profile(t).ProQuality.Degraded {
		t.Fatal("next poll did not honor custom code")
	}
}

func TestProQualityQueuedUnmatchedStatusCancelsWithoutConsumingRetries(t *testing.T) {
	f := newProQualityFixture(t, "merge")
	value, _ := f.s.autoAdminLocks.LoadOrStore(f.admin.TeamAccountID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	f.setMode("error")
	_ = f.s.runProQualityBatch(context.Background(), true)
	_ = f.s.runProQualityBatch(context.Background(), true)
	f.setConnectionMode("upstream_400")
	lock.Unlock()
	f.s.proMergeWG.Wait()
	state := f.profile(t).ProQuality
	if f.connectionTests != 2 || len(f.calls) != 0 || state.Degraded || state.ActionStarted || state.ActionStatus != "cancelled" || state.MergeAttempts != 0 || state.NextMergeAt != nil || state.ConnectionCheck.Status != "unconfirmed" {
		t.Fatalf("unmatched queued recheck was not cancelled: tests=%d calls=%v state=%+v", f.connectionTests, f.calls, state)
	}
}

func TestProQualityConnectionRecoveryDoesNotRecountCachedError(t *testing.T) {
	f := newProQualityFixture(t, "groups")
	f.setMode("probe_error")
	f.setConnectionMode("normal")
	at := time.Now().UTC()
	f.mu.Lock()
	f.probeAt = &at
	f.mu.Unlock()
	f.run(t)
	next := at.Add(time.Second)
	f.mu.Lock()
	f.probeAt = &next
	f.mu.Unlock()
	f.run(t)
	initial := f.profile(t).ProQuality
	if initial.ValidUntil == nil {
		t.Fatalf("second distinct error did not trigger recovery: tests=%d state=%+v", f.connectionTests, initial)
	}
	until := *initial.ValidUntil
	for i := 0; i < 3; i++ {
		f.run(t)
	}
	state := f.profile(t).ProQuality
	if f.connectionTests != 1 || state.Status != "normal" || state.ErrorFailures != 0 || !state.ValidUntil.Equal(until) || f.writes != 0 {
		t.Fatalf("cached error erased recovery or extended validity: %+v", state)
	}
	nextSample := next.Add(time.Second)
	f.mu.Lock()
	f.probeAt = &nextSample
	f.mu.Unlock()
	f.run(t)
	if state = f.profile(t).ProQuality; state.ErrorFailures != 1 || state.Status != "error" || f.connectionTests != 1 {
		t.Fatalf("new error did not start a fresh streak: %+v", state)
	}
}

func TestProQualityQueuedConnectionRecoveryCancelsDisposition(t *testing.T) {
	f := newProQualityFixture(t, "merge")
	value, _ := f.s.autoAdminLocks.LoadOrStore(f.admin.TeamAccountID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	f.setMode("error")
	_ = f.s.runProQualityBatch(context.Background(), true)
	_ = f.s.runProQualityBatch(context.Background(), true)
	f.setConnectionMode("normal")
	lock.Unlock()
	f.s.proMergeWG.Wait()
	state := f.profile(t).ProQuality
	if f.connectionTests != 2 || len(f.calls) != 0 || state.Status != "normal" || state.ActionStarted || state.ActionStatus != "cancelled" || state.ErrorFailures != 0 {
		t.Fatalf("recovered queued account still ran: tests=%d calls=%v state=%+v", f.connectionTests, f.calls, state)
	}
}

func TestProQualityConnectionRecheckHonorsChangesAndCancellation(t *testing.T) {
	for _, change := range []string{"policy", "binding", "stop", "cancel"} {
		t.Run(change, func(t *testing.T) {
			f := newProQualityFixture(t, "groups")
			f.setMode("error")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.connectionHook = func() {
				var err error
				switch change {
				case "policy":
					q := f.policy
					q.Enabled = false
					_, err = f.s.store.SaveProQualitySettings(q)
				case "binding":
					_, err = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.Sub2AccountID = 99 })
				case "stop":
					_, err = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = true })
				case "cancel":
					cancel()
				}
				if err != nil {
					t.Error(err)
				}
			}
			_ = f.s.runProQualityBatch(ctx, true)
			_ = f.s.runProQualityBatch(ctx, true)
			if f.writes != 0 || len(f.calls) != 0 || f.profile(t).ProQuality.ActionStarted {
				t.Fatal("changed or cancelled account was handled")
			}
		})
	}
}

func TestProQualityConfirmedDegradationDoesNotUseConnectionOverride(t *testing.T) {
	f := newProQualityFixture(t, "groups")
	f.setConnectionMode("normal")
	f.run(t)
	if f.connectionTests != 0 || f.writes != 1 || !f.profile(t).ProQuality.Degraded {
		t.Fatal("confirmed quality degradation was treated as a detection error")
	}
}

func TestProQualityQueuedConnectionCheckHonorsStop(t *testing.T) {
	f := newProQualityFixture(t, "merge")
	f.setMode("error")
	f.connectionHook = func() {
		f.mu.Lock()
		count := f.connectionTests
		f.mu.Unlock()
		if count == 2 {
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = true })
			if err != nil {
				t.Error(err)
			}
		}
	}
	_ = f.s.runProQualityBatch(context.Background(), true)
	_ = f.s.runProQualityBatch(context.Background(), true)
	f.s.proMergeWG.Wait()
	if f.connectionTests != 2 || len(f.calls) != 0 || f.profile(t).ProQuality.ActionStarted {
		t.Fatal("stop during queued connection check did not prevent handling")
	}
}

func TestProQualityConnectionRecoveryExpiresAndPolicyResets(t *testing.T) {
	for _, change := range []string{"expired", "policy"} {
		t.Run(change, func(t *testing.T) {
			f := newProQualityFixture(t, "groups")
			f.setMode("probe_error")
			f.setConnectionMode("normal")
			f.run(t)
			f.run(t)
			if f.profile(t).ProQuality.Status != "normal" {
				t.Fatal("setup did not recover")
			}
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				if change == "expired" {
					p.ProQuality.ConnectionCheck.CheckedAt = time.Now().Add(-time.Hour)
				} else {
					p.ProQuality.Revision = "previous-policy"
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			f.run(t)
			state := f.profile(t).ProQuality
			if state.Status == "normal" || f.connectionTests != 1 || f.writes != 0 {
				t.Fatalf("old recovery reused after %s: %+v", change, state)
			}
		})
	}
}
