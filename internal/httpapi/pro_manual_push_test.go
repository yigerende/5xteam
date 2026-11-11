package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func manualBoundPushFixture(t *testing.T) (*Server, string, string, model.ProSettings, *livePushRemote, model.MailAccountProfile) {
	t.Helper()
	old := newLivePushRemote(t)
	s, email, dir, v := livePushFixture(t, old, false)
	p, err := s.performProPush(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Now().Add(-37 * time.Minute)
	p, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProPushStartedAt = &first
		on := true
		p.ProPushTimeoutEnabled = &on
		p.ProPushTimeoutMinutes = 110
		p.CostProvider = "sub2"
		p.CostConnectionIdentity = proCostConnectionIdentity(v.Sub2)
		p.CostDownstreamIdentity = "88"
		p.UserCostDownstreamIdentity = "88"
		p.CostDownstreamSnapshot = 10
		p.UserCostDownstreamSnapshot = 2
		p.CostCheckedAt = &first
		p.Quota5H = &model.FreeQuotaWindow{UsedPercent: 60}
		p.QuotaCheckedAt = &first
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, email, dir, v, old, p
}

func TestProManualPushEndpointsUseCurrentTargetAndKeepClock(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			s, email, _, v, old, before := manualBoundPushFixture(t)
			if batch {
				ended := time.Now().Add(-10 * time.Minute)
				var err error
				before, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProPushEndedAt = &ended })
				if err != nil {
					t.Fatal(err)
				}
			}
			next := newLivePushRemote(t)
			v.Sub2.URL, v.Sub2.GroupIDs = next.url, []int64{99}
			v.Sub2.BindProxy, v.Sub2.ProxyIDs = true, []int64{35}
			v.Sub2.Priority, v.Sub2.AccountConcurrency = 7, 23
			if _, err := s.store.SaveProSettings(v, "new-password", ""); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			if batch {
				s.pushProAccounts(w, httptest.NewRequest("POST", "/api/pro-accounts/push", strings.NewReader(`{"emails":["`+email+`"]}`)))
			} else {
				r := httptest.NewRequest("POST", "/api/pro-accounts/one/push", nil)
				r.SetPathValue("email", email)
				s.pushProAccount(w, r)
			}
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if batch && !strings.Contains(w.Body.String(), `"succeeded":1`) {
				t.Fatal(w.Body.String())
			}
			p, _, err := s.store.MailAccountCredential(email)
			if err != nil {
				t.Fatal(err)
			}
			if len(old.bodies) != 1 || len(next.bodies) != 1 {
				t.Fatal("manual push went to old site")
			}
			var input struct {
				Groups      []int64 `json:"group_ids"`
				ProxyID     int64   `json:"proxy_id"`
				Priority    int     `json:"priority"`
				Concurrency int     `json:"concurrency"`
			}
			if err = json.Unmarshal([]byte(next.bodies[0]), &input); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(input.Groups, []int64{99}) || input.ProxyID != 35 || input.Priority != 7 || input.Concurrency != 23 {
				t.Fatal("stale push settings", input)
			}
			settings, _, _, err := s.proAccountPushSettings(p)
			if err != nil || settings.Sub2.URL != next.url || p.ProPendingPushTargetID != "" || p.Sub2AccountID != 88 || !p.ProPushDownstream.Matches(model.ProDownstream(v)) {
				t.Fatal("new target not committed", err)
			}
			endChanged := (p.ProPushEndedAt == nil) != (before.ProPushEndedAt == nil)
			if p.ProPushEndedAt != nil && before.ProPushEndedAt != nil {
				endChanged = !p.ProPushEndedAt.Equal(*before.ProPushEndedAt)
			}
			if !p.ProPushStartedAt.Equal(*before.ProPushStartedAt) || endChanged || !p.ProPushDeadline().Equal(*before.ProPushDeadline()) {
				t.Fatal("manual push reset original clock or deadline")
			}
			if p.CostCheckedAt != nil || p.UserCostDownstreamIdentity != "" || p.Quota5H != nil {
				t.Fatal("old-site snapshot carried to identical new-site ID")
			}
			user := 99.0
			if _, err = s.saveProCostSnapshot(before, model.ProSettings{Provider: "sub2", Sub2: model.Sub2Settings{URL: old.url, Email: v.Sub2.Email}}, sub2.AccountCosts{StandardCostUSD: 99, UserCostUSD: &user}); err != nil {
				t.Fatal(err)
			}
			p, _, _ = s.store.MailAccountCredential(email)
			if p.CostCheckedAt != nil {
				t.Fatal("late old-site cost overwrote new binding")
			}
		})
	}
}

func TestProManualPushUnknownRetryKeepsNewTargetAndOldWorkingBinding(t *testing.T) {
	s, email, dir, v, old, before := manualBoundPushFixture(t)
	next := newLivePushRemote(t)
	next.status = http.StatusBadGateway
	v.Sub2.URL, v.Sub2.GroupIDs = next.url, []int64{99}
	if _, err := s.store.SaveProSettings(v, "next", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.performProPushMode(context.Background(), email, true); err == nil {
		t.Fatal("expected unknown create")
	}
	p, _, _ := s.store.MailAccountCredential(email)
	working, _, _, err := s.proAccountPushSettings(p)
	if err != nil || working.Sub2.URL != old.url || p.ProRestockTargetID != before.ProRestockTargetID || p.ProPendingPushTargetID == "" || p.PushStatus != "completed" || p.UserCostDownstreamSnapshot != 2 {
		t.Fatal("failed new push destroyed working binding", err)
	}
	// An OAuth/credential save with a stale profile must not erase the request
	// target while its result is unknown, including after a service restart.
	_, creds, err := s.store.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.SaveMailAccount(before, creds); err != nil {
		t.Fatal(err)
	}
	p, _, _ = s.store.MailAccountCredential(email)
	if p.ProPendingPushTargetID == "" {
		t.Fatal("credential save erased uncertain target")
	}
	third := newLivePushRemote(t)
	v.Sub2.URL = third.url
	if _, err = s.store.SaveProSettings(v, "third", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.store.Close(); err != nil {
		t.Fatal(err)
	}
	s.store, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.sub2 = sub2.New()
	next.status = 0
	p, err = s.performProPushMode(context.Background(), email, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(old.bodies) != 1 || len(third.bodies) != 0 || len(next.bodies) != 2 || next.bodies[0] != next.bodies[1] || next.keys[0] != next.keys[1] {
		t.Fatal("uncertain retry moved sites or changed request")
	}
	if !p.ProPushStartedAt.Equal(*before.ProPushStartedAt) || !p.ProPushDeadline().Equal(*before.ProPushDeadline()) {
		t.Fatal("retry reset timing")
	}
}

func TestProManualPushRejectedAttemptCanUseNextSavedTarget(t *testing.T) {
	s, email, _, v, old, _ := manualBoundPushFixture(t)
	next := newLivePushRemote(t)
	next.status = http.StatusBadRequest
	v.Sub2.URL = next.url
	if _, err := s.store.SaveProSettings(v, "next", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.performProPushMode(context.Background(), email, true); err == nil {
		t.Fatal("expected rejection")
	}
	p, _, _ := s.store.MailAccountCredential(email)
	if p.ProPendingPushTargetID != "" || p.PushStatus != "completed" {
		t.Fatal("rejected attempt retained")
	}
	third := newLivePushRemote(t)
	v.Sub2.URL = third.url
	if _, err := s.store.SaveProSettings(v, "third", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.performProPushMode(context.Background(), email, true); err != nil {
		t.Fatal(err)
	}
	if len(old.bodies) != 1 || len(next.bodies) != 1 || len(third.bodies) != 1 {
		t.Fatal("explicit rejection did not allow latest settings")
	}
}
