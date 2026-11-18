package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

type subLinkFixture struct {
	*proExecutionFixture
	logins, syncs, refreshes                                 atomic.Int32
	duplicate, wrongGroup, wrongEmail, changeState, ordinary bool
	noWebID, wrongWebID                                      bool
	remote                                                   string
}

func newSubLinkFixture(t *testing.T) *subLinkFixture {
	t.Helper()
	f := &subLinkFixture{proExecutionFixture: newProExecutionFixture(t)}
	a := func() map[string]any {
		email, group := f.email, int64(7)
		if f.wrongEmail {
			email = "somebody-else@example.test"
		}
		if f.wrongGroup {
			group = 8
		}
		mode := "siwc"
		if f.ordinary {
			mode = "oauth"
		}
		webID := "personal-id"
		if f.noWebID {
			webID = ""
		}
		if f.wrongWebID {
			webID = "different-space"
		}
		return map[string]any{"id": 71, "name": f.email, "platform": "openai", "type": "oauth", "status": "active", "group_ids": []int64{group}, "extra": map[string]any{"auth_provider": mode}, "credentials": map[string]any{"email": email, "auth_mode": mode, "siwc_web_account_id": webID, "access_token": "must-not-copy", "refresh_token": "must-not-copy"}}
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": v}) }
		switch r.URL.Path {
		case "/api/v1/auth/login":
			write(map[string]any{"access_token": "fixture"})
		case "/api/v1/admin/accounts":
			if r.Method != "GET" {
				t.Error("must not create account")
				http.Error(w, "bad", 500)
				return
			}
			if r.URL.Query().Get("search") != f.email {
				t.Error("missing narrowed search")
			}
			items := []any{a()}
			if f.duplicate {
				v := a()
				v["id"] = 72
				items = append(items, v)
			}
			write(map[string]any{"items": items, "total": len(items)})
		case "/api/v1/admin/accounts/71":
			if r.Method != "GET" {
				t.Error("must only read account")
			}
			if f.changeState {
				f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.StopVersion++; p.ProAuto.StopRequested = true })
			}
			write(a())
		case "/api/v1/admin/accounts/71/apply-oauth-credentials":
			f.syncs.Add(1)
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			c, _ := payload["credentials"].(map[string]any)
			if len(c) != 2 || c["siwc_web_access_token"] != proAutoToken(f.email, "new") {
				t.Error("changed remote OAuth credentials")
			}
			write(map[string]any{"id": 71})
		case "/api/v1/admin/siwc/accounts/71/usage", "/api/v1/admin/openai/accounts/71/quota":
			f.refreshes.Add(1)
			write(map[string]any{"checked_at": time.Now(), "rate_limit": map[string]any{"secondary_window": map[string]any{"used_percent": 17, "limit_window_seconds": 604800}}})
		case "/api/v1/admin/accounts/71/stats":
			write(map[string]any{"summary": map[string]any{"total_standard_cost": 0}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(remote.Close)
	f.remote = remote.URL
	v, _, _, _ := f.s.store.ProSettings()
	v.Provider = "sub2"
	v.Sub2.URL, v.Sub2.Email, v.Sub2.AuthMode, v.Sub2.GroupIDs = remote.URL, "admin@example.test", "siwc", []int64{7}
	if _, e := f.s.store.SaveProSettings(v, "test", ""); e != nil {
		t.Fatal(e)
	}
	snapshot := gptpay.Snapshot{}
	snapshot.Input.Session.Account.ID = "personal-id"
	snapshot.Input.Session.User.Email = f.email
	if e := f.s.store.CreateGPTPayOrder(gptpay.Order{ID: "paid-link-order", Email: f.email, Status: "success", CreatedAt: time.Now()}, snapshot); e != nil {
		t.Fatal(e)
	}
	f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
		p.Sub2AccountID = 0
		p.PushProvider = ""
		p.PushStatus = "failed"
		p.CPAAuthFileName = ""
		p.OAuthStatus = "failed"
		p.ProAuto = model.ProAutoState{ID: "link-flow", OrderID: "paid-link-order", Status: "failed", Stage: "oauth", StopVersion: 3, StartedAt: time.Now().Add(-time.Hour), QuotaUsedThreshold: 100, QuotaIntervalSeconds: 60, Steps: map[string]string{"login": "completed", "recharge": "completed", "oauth": "failed"}, StageTimes: map[string]time.Time{"recharge:completed": time.Now().Add(-time.Minute)}}
	})
	f.s.proAutoHooks = &proAutomationHooks{QuotaWebLogin: func(context.Context, string) (map[string]any, error) {
		f.logins.Add(1)
		return map[string]any{"success": true, "access_token": proAutoToken(f.email, "new")}, nil
	}}
	return f
}

func TestProSubLinkResumesOnlyQuotaAndKeepsRemoteOAuth(t *testing.T) {
	f := newSubLinkFixture(t)
	_, beforeCreds, _ := f.s.store.MailAccountCredential(f.email)
	p, e := f.s.performProSubLink(context.Background(), f.email)
	if e != nil {
		t.Fatal(e)
	}
	if p.Sub2AccountID != 71 || p.PushStatus != "completed" || p.ProAuto.Status != "waiting_quota" || p.ProAuto.OrderID != "paid-link-order" || p.ProAuto.OAuthSource != "sub_existing" || p.OAuthStatus != "failed" {
		t.Fatal("bad link state", p.ProAuto)
	}
	_, c, _ := f.s.store.MailAccountCredential(f.email)
	if c.SIWC != nil || c.AccessToken != beforeCreds.AccessToken || c.RefreshToken != beforeCreds.RefreshToken || c.SIWCWebLink == nil {
		t.Fatal("link fabricated or replaced local OAuth")
	}
	started := *p.ProPushStartedAt
	if e = f.s.runProAutomationTail(context.Background(), f.email); e != nil {
		t.Fatal(e)
	}
	p, c, _ = f.s.store.MailAccountCredential(f.email)
	if p.Quota7D == nil || p.Quota7D.UsedPercent != 17 || f.logins.Load() != 1 || f.syncs.Load() != 1 || !c.SIWCWebLink.WebQuota.Synced {
		t.Fatal("linked quota did not refresh")
	}
	if _, e = f.s.performProSubLink(context.Background(), f.email); e != nil {
		t.Fatal(e)
	}
	if e = f.s.runProAutomationTail(context.Background(), f.email); e != nil {
		t.Fatal(e)
	}
	p, c, _ = f.s.store.MailAccountCredential(f.email)
	if f.logins.Load() != 1 || f.syncs.Load() != 1 || !p.ProPushStartedAt.Equal(started) || p.ProAuto.StopVersion != 3 {
		t.Fatal("repeat link reset timing or relogged")
	}
	if _, e = f.s.store.SaveMailAccount(p, model.MailAccountCredentials{Email: f.email, AccessToken: c.AccessToken, RefreshToken: c.RefreshToken}); e != nil {
		t.Fatal(e)
	}
	_, c, _ = f.s.store.MailAccountCredential(f.email)
	if c.SIWCWebLink == nil {
		t.Fatal("lost web link on credential edit")
	}
}

func TestProSubLinkRejectsAmbiguousIdentityAndConcurrentStop(t *testing.T) {
	for _, kind := range []string{"duplicate", "group", "email", "auth_mode", "web_space", "concurrent_stop", "unpaid", "finished"} {
		t.Run(kind, func(t *testing.T) {
			f := newSubLinkFixture(t)
			switch kind {
			case "duplicate":
				f.duplicate = true
			case "group":
				f.wrongGroup = true
			case "email":
				f.wrongEmail = true
			case "auth_mode":
				f.ordinary = true
			case "web_space":
				f.wrongWebID = true
			case "concurrent_stop":
				f.changeState = true
			case "unpaid":
				f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.OrderID = "missing" })
			case "finished":
				f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.SpaceMergedOnce = true })
			}
			_, e := f.s.performProSubLink(context.Background(), f.email)
			if e == nil {
				t.Fatal("unsafe link accepted")
			}
			p, c, _ := f.s.store.MailAccountCredential(f.email)
			if p.Sub2AccountID != 0 || c.SIWCWebLink != nil {
				t.Fatal("failure partially committed")
			}
		})
	}
}

func TestProSubLinkStoppedFlowPinsPaidSpaceWithoutRemoteWebID(t *testing.T) {
	for _, wrongSpace := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_paid_space", true: "reject_other_space"}[wrongSpace], func(t *testing.T) {
			f := newSubLinkFixture(t)
			f.noWebID = true
			_, err := f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) {
				p.ProAuto.StopRequested = true
				p.ProAuto.Status = "stopped"
			})
			if err != nil {
				t.Fatal(err)
			}
			p, err := f.s.performProSubLink(context.Background(), f.email)
			if err != nil {
				t.Fatal(err)
			}
			_, c, _ := f.s.store.MailAccountCredential(f.email)
			if p.ProAuto.StopRequested || p.ProAuto.Status != "waiting_quota" || c.SIWCWebLink.WebAccountID != "personal-id" {
				t.Fatal("stopped paid flow did not resume with pinned paid space")
			}
			if wrongSpace {
				f.s.proAutoHooks.QuotaWebLogin = func(context.Context, string) (map[string]any, error) {
					f.logins.Add(1)
					parts := strings.Split(proAutoToken(f.email, "new"), ".")
					claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
					parts[1] = base64.RawURLEncoding.EncodeToString([]byte(strings.ReplaceAll(string(claims), "personal-id", "wrong-space")))
					return map[string]any{"success": true, "access_token": strings.Join(parts, ".")}, nil
				}
			}
			if err = f.s.runProAutomationTail(context.Background(), f.email); err != nil {
				t.Fatal(err)
			}
			p, c, _ = f.s.store.MailAccountCredential(f.email)
			if wrongSpace {
				if f.syncs.Load() != 0 || p.Quota7D != nil || !strings.Contains(p.ProAuto.Error, "空间") || c.SIWCWebLink.WebAccessToken != "" {
					t.Fatal("wrong paid space was accepted or sent to Sub")
				}
			} else if f.syncs.Load() != 1 || p.Quota7D == nil || p.Quota7D.UsedPercent != 17 {
				t.Fatal("correct paid space did not refresh quota")
			}
		})
	}
}

func TestProSubLinkOrdinaryOAuthUsesOriginalQuota(t *testing.T) {
	f := newSubLinkFixture(t)
	f.ordinary = true
	v, pw, key, _ := f.s.store.ProSettings()
	v.Sub2.AuthMode = ""
	if _, e := f.s.store.SaveProSettings(v, pw, key); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.performProSubLink(context.Background(), f.email); e != nil {
		t.Fatal(e)
	}
	if e := f.s.runProAutomationTail(context.Background(), f.email); e != nil {
		t.Fatal(e)
	}
	p, c, _ := f.s.store.MailAccountCredential(f.email)
	if p.Quota7D == nil || c.SIWCWebLink != nil || f.logins.Load() != 0 || f.syncs.Load() != 0 {
		t.Fatal("ordinary OAuth was converted to SIWC")
	}
}

func TestProSubLinkHTTPQueuesQuotaWithoutBlockingAndBusyConflict(t *testing.T) {
	f := newSubLinkFixture(t)
	unlock := f.s.lockProAccount(f.email)
	w := f.request(f.s.linkProSubAccount, "POST", "/link-sub", "")
	unlock()
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	w = f.request(f.s.linkProSubAccount, "POST", "/link-sub", "")
	if w.Code != 202 || strings.Contains(w.Body.String(), "must-not-copy") {
		t.Fatal(w.Body.String())
	}
	p := waitProAutoFinished(t, f.s, f.email)
	if p.ProAuto.Status != "waiting_quota" || p.Quota7D == nil || f.logins.Load() != 1 {
		t.Fatal("async tail failed", p.ProAuto)
	}
}
