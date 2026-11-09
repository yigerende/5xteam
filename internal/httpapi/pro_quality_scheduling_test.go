package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func (f *proQualityFixture) enablePause(t *testing.T) {
	t.Helper()
	f.policy.PauseBeforeAction = true
	var err error
	f.policy, err = f.s.store.SaveProQualitySettings(f.policy)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *proQualityFixture) configureSIWC(t *testing.T) {
	t.Helper()
	cfg := model.DefaultProSIWCSettings()
	cfg.Sub2.URL, cfg.Sub2.Email = f.cfg.Sub2.URL, "siwc@example.test"
	cfg.Sub2.GroupIDs, cfg.MonitorGroupIDs = []int64{7}, []int64{7}
	if _, err := f.s.store.SaveProSIWCSettings(cfg, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestProQualityPauseBeforeEveryAction(t *testing.T) {
	for _, action := range []string{"groups", "merge", "siwc"} {
		t.Run(action, func(t *testing.T) {
			f := newProQualityFixture(t, action)
			f.enablePause(t)
			if action == "siwc" {
				f.configureSIWC(t)
			}
			// No downstream handling may have started when the pause is requested.
			f.pauseHook = func() {
				p := f.profile(t)
				if p.ProSIWC != nil || p.ProQuality.ActionStarted || f.writes != 0 || len(f.calls) != 0 {
					t.Error("degradation handling started before scheduling pause")
				}
			}
			f.run(t)
			f.s.proMergeWG.Wait()
			p := f.profile(t)
			if f.pauseCalls != 1 || f.schedulable {
				t.Fatalf("pause not applied once: %d", f.pauseCalls)
			}
			switch action {
			case "groups":
				if !strings.Contains(strings.Join(f.events, ","), "pause,read,groups") || !p.ProQuality.Routed {
					t.Fatalf("wrong group sequence: %v", f.events)
				}
			case "merge":
				if p.ProQuality.ActionStatus != "completed" || f.calls["invite"] != 1 {
					t.Fatalf("merge not completed: %+v", p.ProQuality)
				}
			case "siwc":
				if p.ProSIWC == nil || p.ProSIWC.Status != "queued" {
					t.Fatal("SIWC was not queued after pause")
				}
			}
		})
	}
}

func TestProQualityPauseFailureBlocksAndRetriesEveryAction(t *testing.T) {
	for _, action := range []string{"groups", "merge", "siwc"} {
		for _, mode := range []string{"fail", "ignored", "missing"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				f := newProQualityFixture(t, action)
				f.enablePause(t)
				if action == "siwc" {
					f.configureSIWC(t)
				}
				f.pauseMode = mode
				f.run(t)
				f.s.proMergeWG.Wait()
				p := f.profile(t)
				if f.writes != 0 || len(f.calls) != 0 || p.ProSIWC != nil || p.ProQuality.MergeAttempts != 0 || p.ProQuality.ActionStatus != "failed" || !strings.Contains(p.ProQuality.ActionError, "暂停 Sub2 调度失败") {
					t.Fatalf("pause failure did not block %s: %+v", action, p.ProQuality)
				}
				f.mu.Lock()
				f.pauseMode = ""
				f.mu.Unlock()
				f.run(t)
				f.s.proMergeWG.Wait()
				p = f.profile(t)
				if f.schedulable || p.ProQuality.ActionStatus == "failed" || (action == "siwc" && p.ProSIWC == nil) {
					t.Fatalf("retry did not recover: %+v", p.ProQuality)
				}
			})
		}
	}
}

func TestProQualityPauseAmbiguousOrAlreadyPaused(t *testing.T) {
	for _, mode := range []string{"ambiguous", "already_paused"} {
		t.Run(mode, func(t *testing.T) {
			f := newProQualityFixture(t, "groups")
			f.enablePause(t)
			f.pauseMode = mode
			if mode == "already_paused" {
				f.schedulable = false
			}
			f.run(t)
			if !f.profile(t).ProQuality.Routed || f.schedulable {
				t.Fatal("verified paused account did not proceed")
			}
			if mode == "already_paused" && f.pauseCalls != 0 {
				t.Fatal("already paused account was rewritten")
			}
			f.setMode("normal")
			f.run(t)
			if f.schedulable || f.profile(t).ProQuality.Routed {
				t.Fatal("group recovery must not reopen scheduling")
			}
		})
	}
}

func TestProQualityPauseRequiresConfirmedCurrentDegradation(t *testing.T) {
	for _, mode := range []string{"normal", "stale", "unknown", "disabled", "old_streak", "revision", "clock", "error", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			f := newProQualityFixture(t, "groups")
			f.enablePause(t)
			f.setMode(mode)
			if mode == "stopped" {
				f.setMode("bad")
				_, _ = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.StopRequested = true })
			}
			_ = f.s.runProQualityBatch(context.Background(), true)
			if f.pauseCalls != 0 || f.writes != 0 {
				t.Fatal("paused an unconfirmed or stopped account")
			}
		})
	}
	f := newProQualityFixture(t, "groups")
	f.enablePause(t)
	f.setMode("error")
	f.setConnectionMode("normal")
	for i := 0; i < 2; i++ {
		_ = f.s.runProQualityBatch(context.Background(), true)
	}
	if f.pauseCalls != 0 || f.writes != 0 || f.connectionTests != 1 {
		t.Fatal("connection recovery still paused scheduling")
	}
}

func TestProQualityPauseRevalidatesAfterNetworkWait(t *testing.T) {
	for _, action := range []string{"groups", "merge", "siwc"} {
		t.Run(action, func(t *testing.T) {
			f := newProQualityFixture(t, action)
			f.enablePause(t)
			if action == "siwc" {
				f.configureSIWC(t)
			}
			f.pauseHook = func() {
				q := f.policy
				q.Enabled = false
				if _, err := f.s.store.SaveProQualitySettings(q); err != nil {
					t.Error(err)
				}
			}
			f.run(t)
			f.s.proMergeWG.Wait()
			if f.writes != 0 || len(f.calls) != 0 || f.profile(t).ProSIWC != nil {
				t.Fatal("changed policy still dispatched follow-up")
			}
		})
	}
}

func TestProQualityQueuedPauseFailureDoesNotConsumeMergeRetry(t *testing.T) {
	f := newProQualityFixture(t, "merge")
	f.enablePause(t)
	value, _ := f.s.autoAdminLocks.LoadOrStore(f.admin.TeamAccountID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	f.run(t)
	f.mu.Lock()
	f.schedulable, f.pauseMode = true, "fail"
	f.mu.Unlock()
	lock.Unlock()
	f.s.proMergeWG.Wait()
	p := f.profile(t)
	if len(f.calls) != 0 || p.ProQuality.MergeAttempts != 0 || p.ProQuality.ActionStarted || p.ProQuality.ActionStatus != "failed" {
		t.Fatalf("queued pause failure consumed a merge: %+v", p.ProQuality)
	}
}

func TestProQualityPauseSettingRoundTripAndDefault(t *testing.T) {
	f := newProQualityFixture(t, "groups")
	if f.policy.PauseBeforeAction {
		t.Fatal("existing configurations must default off")
	}
	for _, enabled := range []bool{true, false} {
		q := f.policy
		q.PauseBeforeAction = enabled
		body, _ := json.Marshal(q)
		w := f.request(f.s.saveProQualitySettings, "PUT", "/api/pro-quality/settings", string(body))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		stored, err := f.s.store.ProQualitySettings()
		if err != nil || stored.PauseBeforeAction != enabled {
			t.Fatal("pause setting did not persist")
		}
		w = f.request(f.s.getProQualitySettings, "GET", "/api/pro-quality/settings", "")
		var response struct {
			Data struct {
				Settings model.ProQualitySettings `json:"settings"`
			} `json:"data"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data.Settings.PauseBeforeAction != enabled {
			t.Fatalf("pause setting missing from response: %s", w.Body.String())
		}
	}
	f.run(t)
	if f.pauseCalls != 0 || f.writes != 1 || !f.schedulable {
		t.Fatal("disabled switch changed original handling")
	}
}
