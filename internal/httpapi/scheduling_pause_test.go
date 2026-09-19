package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestSchedulingPauseQuotaRemoval(t *testing.T) {
	for _, tc := range []struct {
		name                                                                                                                                                string
		seconds                                                                                                                                             int64
		timeout                                                                                                                                             int
		expectedQueries                                                                                                                                     int32
		remove, disabledQuota, disabledAccount, queryFailed, missingTime, wrongEmail, wrongID, resumed, repaused, changedSetting, cleanupFailed, childLeave bool
	}{
		{name: "disabled makes no calls", timeout: 0},
		{name: "below threshold", timeout: 300, seconds: 299, expectedQueries: 1},
		{name: "at threshold", timeout: 300, seconds: 300, expectedQueries: 1},
		{name: "expired removes and deletes", timeout: 300, seconds: 301, expectedQueries: 2, remove: true},
		{name: "quota disabled", timeout: 300, seconds: 400, disabledQuota: true},
		{name: "account auto removal disabled", timeout: 300, seconds: 400, disabledAccount: true},
		{name: "API unavailable keeps quota workflow", timeout: 300, seconds: 400, expectedQueries: 1, queryFailed: true},
		{name: "missing timestamp", timeout: 300, seconds: 400, expectedQueries: 1, missingTime: true},
		{name: "wrong identity", timeout: 300, seconds: 400, expectedQueries: 1, wrongEmail: true},
		{name: "wrong account ID", timeout: 300, seconds: 400, expectedQueries: 1, wrongID: true},
		{name: "resumed before removal", timeout: 300, seconds: 400, expectedQueries: 2, resumed: true},
		{name: "new pause before removal", timeout: 300, seconds: 600, expectedQueries: 2, repaused: true},
		{name: "disable during query", timeout: 300, seconds: 400, expectedQueries: 1, changedSetting: true},
		{name: "cleanup retry never kicks twice", timeout: 300, seconds: 400, expectedQueries: 2, remove: true, cleanupFailed: true},
		{name: "uses configured child leave", timeout: 300, seconds: 400, expectedQueries: 2, remove: true, childLeave: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			var pauses, kicks, deletes, quotas atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("unexpected space request: %s %s", r.Method, r.URL)
				}
				if !tc.childLeave && r.Header.Get("Authorization") != "Bearer mother-token" {
					t.Error("wrong mother authorization")
				}
				if tc.childLeave && r.Header.Get("Authorization") != "Bearer child-token" {
					t.Error("wrong child authorization")
				}
				kicks.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			now := time.Now().UTC()
			downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/v1/auth/login":
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin-token"}}`)
				case r.URL.Path == "/api/v1/admin/accounts/scheduling-paused":
					count := pauses.Add(1)
					if r.Method != "GET" || r.URL.Query().Get("account_ids") != "42" {
						t.Error("pause query not scoped to linked account")
					}
					if tc.queryFailed {
						http.Error(w, "not supported", 404)
						return
					}
					seconds := tc.seconds
					if tc.repaused && count == 2 {
						seconds = 401
					}
					at := now.Add(-time.Duration(seconds) * time.Second)
					accountID := int64(42)
					email := "child@example.com"
					if tc.wrongID {
						accountID = 43
					}
					if tc.wrongEmail {
						email = "other@example.com"
					}
					var atValue any = at
					if tc.missingTime {
						atValue = nil
					}
					accounts := []any{map[string]any{"account_id": accountID, "platform": "openai", "type": "oauth", "email": email, "scheduling_paused_at": atValue, "paused_seconds": seconds}}
					if tc.resumed && count == 2 {
						accounts = []any{}
					}
					if tc.changedSetting {
						setting, _, readErr := st.Sub2Settings()
						if readErr != nil {
							t.Error(readErr)
						}
						setting.SchedulingPauseTimeoutSeconds = 0
						if _, saveErr := st.SaveSub2Settings(setting, ""); saveErr != nil {
							t.Error(saveErr)
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"version": 1, "server_now": now, "accounts": accounts, "has_more": false}})
				case r.Method == http.MethodDelete:
					if r.URL.Path != "/api/v1/admin/accounts/42" || kicks.Load() != 1 {
						t.Error("incorrect deletion identity or order")
					}
					n := deletes.Add(1)
					if tc.cleanupFailed && n == 1 {
						http.Error(w, "cleanup failure", 502)
						return
					}
					fmt.Fprint(w, `{"code":0,"data":{}}`)
				case strings.HasSuffix(r.URL.Path, "/stats"):
					fmt.Fprint(w, `{"code":0,"data":{"summary":{"total_standard_cost":12}}}`)
				case strings.HasSuffix(r.URL.Path, "/quota"):
					quotas.Add(1)
					fmt.Fprint(w, `{"code":0,"data":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":10,"limit_window_seconds":604800}}}}`)
				default:
					t.Errorf("unexpected downstream request: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer downstream.Close()
			settings := model.DefaultSettings()
			settings.BaseURL = upstream.URL
			if err := st.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			rotation := model.DefaultAutoRotationSettings()
			rotation.TeamOperationIntervalSeconds = 0
			if _, err := st.SaveAutoRotationSettings(rotation); err != nil {
				t.Fatal(err)
			}
			push := model.DefaultSub2Settings()
			push.URL, push.Email = downstream.URL, "admin@example.com"
			push.SchedulingPauseTimeoutSeconds = tc.timeout
			push.QuotaEnabled = !tc.disabledQuota
			if _, err := st.SaveSub2Settings(push, "password"); err != nil {
				t.Fatal(err)
			}
			admin := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "mother", TeamAccountID: "team"}, "mother-token", upstream.URL)
			child, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "child@example.com", UserID: "child"}, "child-token")
			if err != nil {
				t.Fatal(err)
			}
			if tc.childLeave {
				if _, err := st.SaveMailAccount(model.MailAccountProfile{Email: child.Email}, model.MailAccountCredentials{Email: child.Email, AccessToken: "child-token"}); err != nil {
					t.Fatal(err)
				}
			}
			_, err = st.UpdateFreeAccount(child.ID, func(p *model.FreeAccountProfile) {
				p.AdminAccountID, p.TeamAccountID = admin.ID, "team"
				p.AcceptStatus, p.PushStatus, p.RemoveStatus = "completed", "completed", "pending"
				p.PushProvider, p.Sub2AccountID, p.AutoRemove = "sub2", 42, !tc.disabledAccount
				p.RemoveMethod = "mother_kick"
				if tc.childLeave {
					p.RemoveMethod = "child_leave"
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			s, err := New(st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, _, checkErr := s.performFreeAccountQuota(t.Context(), child.ID, true)
			got, _, err := st.FreeAccountCredential(child.ID)
			if err != nil {
				t.Fatal(err)
			}
			if pauses.Load() != tc.expectedQueries {
				t.Fatalf("pause queries=%d want=%d", pauses.Load(), tc.expectedQueries)
			}
			if !tc.remove {
				if checkErr != nil || kicks.Load() != 0 || deletes.Load() != 0 || quotas.Load() != 1 {
					t.Fatalf("original quota path changed: %v kicks=%d deletes=%d quotas=%d", checkErr, kicks.Load(), deletes.Load(), quotas.Load())
				}
				return
			}
			if kicks.Load() != 1 || deletes.Load() != 1 || quotas.Load() != 0 || !strings.Contains(got.RemovalReason, "暂停调度") {
				t.Fatalf("removal missing or repeated: %+v err=%v", got, checkErr)
			}
			if tc.cleanupFailed {
				if checkErr == nil || got.RemoveStatus != "cleanup_pending" || got.RemoteRemovedAt == nil {
					t.Fatal("cleanup failure lost pending state")
				}
				got, checkErr = s.performFreeAccountRemove(t.Context(), child.ID)
				if kicks.Load() != 1 || deletes.Load() != 2 {
					t.Fatal("cleanup retry repeated space removal")
				}
			}
			if checkErr != nil || got.RemoveStatus != "completed" || !got.DownstreamCleaned {
				t.Fatalf("removal not completed: %+v %v", got, checkErr)
			}
		})
	}
}

func TestSchedulingPauseSettingsBothRoutesAndPersistence(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	s := &Server{store: st}
	base := `"url":"http://sub2.example.com","email":"admin@example.com","password":"password"`
	put := func(combined bool, field string, wantStatus int) {
		t.Helper()
		body := "{" + base + field + "}"
		if combined {
			body = `{"provider":"sub2","sub2":` + body + "}"
		}
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(body))
		if combined {
			s.savePushSettings(recorder, req)
		} else {
			s.saveSub2Settings(recorder, req)
		}
		if recorder.Code != wantStatus {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	for _, combined := range []bool{true, false} {
		put(combined, `,"scheduling_pause_timeout_seconds":600`, 200)
		put(!combined, "", 200)
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		st, err = store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		s.store = st
		saved, _, err := st.Sub2Settings()
		if err != nil || saved.SchedulingPauseTimeoutSeconds != 600 {
			t.Fatal("omitted setting or restart lost timeout")
		}
		put(combined, `,"scheduling_pause_timeout_seconds":0`, 200)
		saved, _, _ = st.Sub2Settings()
		if saved.SchedulingPauseTimeoutSeconds != 0 {
			t.Fatal("explicit zero did not disable")
		}
		put(combined, `,"scheduling_pause_timeout_seconds":-1`, 400)
	}
}
