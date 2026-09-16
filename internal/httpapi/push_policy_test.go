package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestZeroReloginLimitRemovesOnlyAccount401(t *testing.T) {
	for _, tc := range []struct {
		name, provider, probe, response  string
		status                           int
		remove, cleanupFailure, disabled bool
	}{
		{name: "sub2 status", provider: "sub2", probe: "status", status: 200, response: `{"code":0,"data":{"http_status":401}}`, remove: true},
		{name: "sub2 quota", provider: "sub2", probe: "quota", status: 401, response: `{"code":401,"reason":"OPENAI_QUOTA_UPSTREAM_ERROR","message":"upstream returned 401"}`, remove: true},
		{name: "sub2 nested quota", provider: "sub2", probe: "quota", status: 200, response: `{"code":0,"data":{"status_code":401}}`, remove: true},
		{name: "sub2 admin status 401", provider: "sub2", probe: "status", status: 401, response: `{"code":401,"message":"unauthorized"}`},
		{name: "sub2 admin quota 401", provider: "sub2", probe: "quota", status: 401, response: `{"code":401,"message":"unauthorized"}`},
		{name: "sub2 quota timeout", provider: "sub2", probe: "quota", status: 502, response: `{"code":502,"reason":"OPENAI_QUOTA_REQUEST_FAILED","message":"context deadline exceeded"}`},
		{name: "401 detection disabled", provider: "sub2", probe: "quota", status: 401, response: `{"code":401,"reason":"OPENAI_QUOTA_UPSTREAM_ERROR","message":"upstream returned 401"}`, disabled: true},
		{name: "cpa status", provider: "cpa", probe: "status", status: 200, response: `{"status_code":401,"body":"unauthorized"}`, remove: true},
		{name: "cpa admin 401", provider: "cpa", probe: "status", status: 401, response: `{"error":"unauthorized"}`},
		{name: "cpa upstream 403", provider: "cpa", probe: "status", status: 200, response: `{"status_code":403,"body":"forbidden"}`},
		{name: "sub2 cleanup retry", provider: "sub2", probe: "status", status: 200, response: `{"code":0,"data":{"http_status":401}}`, remove: true, cleanupFailure: true},
		{name: "cpa cleanup retry", provider: "cpa", probe: "status", status: 200, response: `{"status_code":401,"body":"unauthorized"}`, remove: true, cleanupFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			var kicks, deletes atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/accounts/team-policy/users/child-policy" {
					t.Errorf("unexpected Team request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if r.Header.Get("Authorization") != "Bearer mother-token" {
					t.Error("did not use mother token")
				}
				kicks.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/v1/auth/login":
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin-token"}}`)
				case r.Method == http.MethodDelete:
					if kicks.Load() != 1 {
						t.Error("downstream deleted before mother kick")
					}
					if tc.provider == "cpa" && (r.URL.Path != "/v0/management/auth-files" || r.URL.Query().Get("name") != "child.json") {
						t.Error("wrong CPA file")
					}
					if tc.provider == "sub2" && r.URL.Path != "/api/v1/admin/accounts/42" {
						t.Error("wrong Sub2 account")
					}
					n := deletes.Add(1)
					if tc.cleanupFailure && n == 1 {
						w.WriteHeader(502)
						fmt.Fprint(w, `{"error":"temporary cleanup failure"}`)
						return
					}
					fmt.Fprint(w, `{"code":0,"data":{}}`)
				case strings.HasSuffix(r.URL.Path, "/stats"):
					fmt.Fprint(w, `{"code":0,"data":{"summary":{"total_standard_cost":12}}}`)
				case r.URL.Path == "/v0/management/auth-files":
					fmt.Fprint(w, `{"files":[{"name":"child.json","auth_index":"child-index","account_id":"team-policy"}]}`)
				case r.URL.Path == "/api/v1/admin/accounts/42" || strings.HasSuffix(r.URL.Path, "/quota") || r.URL.Path == "/v0/management/api-call":
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.response)
				default:
					t.Errorf("unexpected downstream request: %s %s", r.Method, r.URL.Path)
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
			rotation.RemoveMethod = "child_leave"
			if _, err := st.SaveAutoRotationSettings(rotation); err != nil {
				t.Fatal(err)
			}
			push := model.DefaultSub2Settings()
			push.URL, push.Email, push.Provider = downstream.URL, "admin@example.com", tc.provider
			push.ReloginFailureLimit = 0
			push.Enable401Check = !tc.disabled
			if _, err := st.SaveSub2Settings(push, "password"); err != nil {
				t.Fatal(err)
			}
			cpa := model.DefaultCPASettings()
			cpa.URL, cpa.ReloginFailureLimit = downstream.URL, 0
			if _, err := st.SaveCPASettings(cpa, "key"); err != nil {
				t.Fatal(err)
			}
			admin := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "mother", TeamAccountID: "team-policy"}, "mother-token", upstream.URL)
			child, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "child@example.com", UserID: "child-policy"}, "expired-child-token")
			if err != nil {
				t.Fatal(err)
			}
			_, err = st.UpdateFreeAccount(child.ID, func(p *model.FreeAccountProfile) {
				p.AdminAccountID, p.TeamAccountID = admin.ID, "team-policy"
				p.AcceptStatus, p.PushStatus, p.RemoveStatus = "completed", "completed", "pending"
				p.RemoveMethod, p.PushProvider = "child_leave", tc.provider
				p.Sub2AccountID, p.CPAAuthFileName = 42, "child.json"
			})
			if err != nil {
				t.Fatal(err)
			}
			s, err := New(st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if tc.probe == "quota" {
				_, _, err = s.performFreeAccountQuota(t.Context(), child.ID, true)
			} else {
				_, err = s.checkFreeAccountStatus(t.Context(), child.ID, push, "password")
			}
			got, _, readErr := st.FreeAccountCredential(child.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(s.oauthJobs) != 0 || got.ReloginCount != 0 || got.ReloginFailureCount != 0 {
				t.Fatal("zero limit launched or counted a relogin")
			}
			if !tc.remove {
				if kicks.Load() != 0 || deletes.Load() != 0 || got.ReloginExhausted || got.RemoveStatus == "completed" {
					t.Fatalf("non-account-401 caused removal: %+v", got)
				}
				return
			}
			if kicks.Load() != 1 || deletes.Load() != 1 || got.RemoveMethod != "mother_kick" || !got.ReloginExhausted {
				t.Fatalf("missing forced kick/cleanup: kicks=%d deletes=%d profile=%+v err=%v", kicks.Load(), deletes.Load(), got, err)
			}
			if tc.cleanupFailure {
				if err == nil || got.RemoveStatus != "cleanup_pending" || got.RemoteRemovedAt == nil || got.DownstreamCleaned {
					t.Fatalf("cleanup failure lost retry state: %+v err=%v", got, err)
				}
				got, err = s.performFreeAccountRemove(t.Context(), child.ID)
				if kicks.Load() != 1 || deletes.Load() != 2 {
					t.Fatal("cleanup retry repeated mother kick or skipped deletion")
				}
			}
			if err != nil || got.RemoveStatus != "completed" || !got.DownstreamCleaned {
				t.Fatalf("removal incomplete: %+v err=%v", got, err)
			}
		})
	}
}

func TestPushSettingsPreserveZeroAndPlan(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	s := &Server{store: st}
	put := func(path, body string, status int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		if path == "/api/sub2-settings" {
			s.saveSub2Settings(rec, req)
		} else {
			s.savePushSettings(rec, req)
		}
		if rec.Code != status {
			t.Fatalf("settings status=%d want=%d: %s", rec.Code, status, rec.Body.String())
		}
	}
	base := `"url":"http://sub2.example.com","email":"admin@example.com","password":"password"`
	put("/api/push-settings", `{"provider":"sub2","sub2":{`+base+`,"push_plan_type":"pro","relogin_failure_limit":0},"cpa":{"url":"http://cpa.example.com","key":"key","relogin_failure_limit":0}}`, 200)
	// Older callers omit the fields; omission must not be treated as explicit zero.
	put("/api/push-settings", `{"provider":"sub2","sub2":{`+base+`},"cpa":{}}`, 200)
	put("/api/sub2-settings", `{`+base+`}`, 200)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.store = st
	sub, _, err := st.Sub2Settings()
	if err != nil || sub.ReloginFailureLimit != 0 || sub.PushPlanType != "pro" {
		t.Fatalf("zero/Pro lost after restart: %+v %v", sub, err)
	}
	cpa, _, err := st.CPASettings()
	if err != nil || cpa.ReloginFailureLimit != 0 {
		t.Fatalf("CPA zero lost: %+v %v", cpa, err)
	}
	put("/api/sub2-settings", `{`+base+`,"relogin_failure_limit":3,"push_plan_type":"self_serve_business_prolite"}`, 200)
	put("/api/sub2-settings", `{`+base+`}`, 200)
	sub, _, _ = st.Sub2Settings()
	if sub.ReloginFailureLimit != 3 || sub.PushPlanType != downstreamProlitePlanType {
		t.Fatal("omitted fields changed existing settings")
	}
	put("/api/sub2-settings", `{`+base+`,"relogin_failure_limit":0}`, 200)
	sub, _, _ = st.Sub2Settings()
	if sub.ReloginFailureLimit != 0 {
		t.Fatal("explicit zero did not replace positive limit")
	}
	put("/api/push-settings", `{"provider":"sub2","sub2":{`+base+`,"relogin_failure_limit":-1}}`, 400)
	put("/api/sub2-settings", `{`+base+`,"relogin_failure_limit":21}`, 400)
	put("/api/push-settings", `{"provider":"sub2","sub2":{`+base+`,"push_plan_type":"plus"}}`, 400)
}

func TestSub2PushUsesSelectedPlan(t *testing.T) {
	for _, plan := range []string{downstreamProlitePlanType, "pro"} {
		t.Run(plan, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			var sent map[string]any
			downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/auth/login" {
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin-token"}}`)
					return
				}
				if r.URL.Path != "/api/v1/admin/accounts" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				_ = json.NewDecoder(r.Body).Decode(&sent)
				fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"child"}}`)
			}))
			defer downstream.Close()
			push := model.DefaultSub2Settings()
			push.URL, push.Email, push.PushPlanType, push.GroupIDs = downstream.URL, "admin@example.com", plan, []int64{1}
			if _, err := st.SaveSub2Settings(push, "password"); err != nil {
				t.Fatal(err)
			}
			child, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "child@example.com", UserID: "child-policy"}, "source")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.SaveFreeAccountOAuth(child.ID, "oauth-at", "oauth-rt", "team-policy"); err != nil {
				t.Fatal(err)
			}
			s, err := New(st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			req := httptest.NewRequest(http.MethodPost, "/api/free-accounts/"+child.ID+"/push", nil)
			req.SetPathValue("id", child.ID)
			rec := httptest.NewRecorder()
			s.pushFreeAccount(rec, req)
			if rec.Code != 200 {
				t.Fatalf("push failed: %s", rec.Body.String())
			}
			credentials, ok := sent["credentials"].(map[string]any)
			if !ok || credentials["plan_type"] != plan || credentials["chatgpt_plan_type"] != plan {
				t.Fatalf("wrong pushed plan: %+v", sent)
			}
			renewed := buildSub2OAuthCredentialsWithModels(child, store.FreeAccountCredentials{OAuthAccessToken: "new-at", OAuthRefreshToken: "new-rt"}, []string{"gpt-5"}, push.PushPlanType)
			if renewed["plan_type"] != plan || renewed["chatgpt_plan_type"] != plan || renewed["model_mapping"] == nil {
				t.Fatalf("renewal lost selected plan or models: %+v", renewed)
			}
		})
	}
}
