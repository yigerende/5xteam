package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"chatgpt-space-merge/internal/cpa"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func TestProConnectionUsesDraftWithoutSaving(t *testing.T) {
	for _, provider := range []string{"sub2", "cpa"} {
		for _, action := range []string{"groups", "test"} {
			for _, mode := range []string{"new", "override", "saved-secret", "legacy-empty", "legacy-get", "invalid"} {
				if mode == "legacy-get" && action != "groups" {
					continue
				}
				t.Run(provider+"/"+action+"/"+mode, func(t *testing.T) {
					st, err := store.Open(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					defer st.Close()
					var calls atomic.Int32
					secret := "draft-secret"
					if mode == "saved-secret" || strings.HasPrefix(mode, "legacy") {
						secret = "saved-secret"
					}
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						w.Header().Set("Content-Type", "application/json")
						switch r.URL.Path {
						case "/api/v1/auth/login":
							var login map[string]string
							if err := json.NewDecoder(r.Body).Decode(&login); err != nil {
								t.Error(err)
							}
							if login["email"] != "admin@example.com" || login["password"] != secret {
								t.Errorf("wrong draft credentials: email=%q", login["email"])
								http.Error(w, "invalid credentials", 401)
								return
							}
							fmt.Fprint(w, `{"code":0,"data":{"access_token":"test-session"}}`)
						case "/api/v1/admin/groups/all":
							if r.Header.Get("Authorization") != "Bearer test-session" {
								t.Error("missing login token")
							}
							fmt.Fprint(w, `{"code":0,"data":[{"id":7,"name":"Pro OpenAI","platform":"openai"},{"id":8,"name":"Other","platform":"anthropic"}]}`)
						case "/v0/management/account-groups":
							if r.Header.Get("Authorization") != "Bearer "+secret {
								t.Error("wrong management key")
							}
							fmt.Fprint(w, `{"groups":[{"id":9,"name":"Pro CPA"}]}`)
						case "/v0/management/auth-files":
							if r.Header.Get("Authorization") != "Bearer "+secret {
								t.Error("wrong management key")
							}
							fmt.Fprint(w, `{"files":[]}`)
						default:
							t.Errorf("unexpected upstream path: %s", r.URL.Path)
							http.NotFound(w, r)
						}
					}))
					defer upstream.Close()
					if mode != "new" {
						saved := model.DefaultProSettings()
						saved.Provider = "cpa"
						saved.Sub2.URL, saved.Sub2.Email = upstream.URL, "admin@example.com"
						saved.CPA.URL = upstream.URL
						if mode == "override" {
							saved.Sub2.URL, saved.CPA.URL = "https://unused.invalid", "https://unused.invalid"
						}
						saved.Sub2.GroupIDs, saved.Sub2.GroupNames = []int64{42}, []string{"Saved"}
						if _, err := st.SaveProSettings(saved, "saved-secret", "saved-secret"); err != nil {
							t.Fatal(err)
						}
					}
					before, beforePassword, beforeKey, err := st.ProSettings()
					if err != nil {
						t.Fatal(err)
					}
					s := &Server{store: st, sub2: sub2.New(), cpa: cpa.New()}
					body := map[string]any{
						"sub2":          map[string]any{"url": "  " + upstream.URL + "/ ", "email": " admin@example.com "},
						"cpa":           map[string]any{"url": " " + upstream.URL + "/ "},
						"sub2_password": "draft-secret", "cpa_key": "draft-secret",
					}
					if mode == "saved-secret" {
						body["sub2_password"], body["cpa_key"] = "", ""
					}
					if mode == "invalid" {
						body[provider] = map[string]any{"url": "invalid-url"}
					}
					encoded, _ := json.Marshal(body)
					method, raw := "POST", string(encoded)
					if mode == "legacy-empty" {
						raw = ""
					}
					if mode == "legacy-get" {
						method, raw = "GET", ""
					}
					req := httptest.NewRequest(method, "/api/pro-settings/"+provider+"/"+action, strings.NewReader(raw))
					out := httptest.NewRecorder()
					handler := s.getProSub2Groups
					if provider == "sub2" && action == "test" {
						handler = s.testProSub2
					}
					if provider == "cpa" && action == "groups" {
						handler = s.getProCPAGroups
					}
					if provider == "cpa" && action == "test" {
						handler = s.testProCPA
					}
					handler(out, req)
					if mode == "invalid" {
						if out.Code != 400 || calls.Load() != 0 {
							t.Fatalf("invalid URL was not rejected: %d calls=%d", out.Code, calls.Load())
						}
					} else {
						if out.Code != 200 {
							t.Fatalf("draft connection: %d %s", out.Code, out.Body.String())
						}
						if calls.Load() == 0 {
							t.Fatal("upstream was not called")
						}
						if action == "groups" || provider == "sub2" {
							if !strings.Contains(out.Body.String(), "Pro ") || strings.Contains(out.Body.String(), "Other") {
								t.Fatalf("wrong groups: %s", out.Body.String())
							}
						}
					}
					if strings.Contains(out.Body.String(), secret) {
						t.Fatal("response leaked credentials")
					}
					after, afterPassword, afterKey, err := st.ProSettings()
					if err != nil || !reflect.DeepEqual(before, after) || beforePassword != afterPassword || beforeKey != afterKey {
						t.Fatal("connection check modified saved settings or secrets")
					}
					// First-time configuration can now read groups before satisfying save validation.
					if mode == "new" {
						payload := model.DefaultProSettings()
						payload.Provider = provider
						payload.Sub2.URL, payload.Sub2.Email, payload.Sub2.GroupIDs = upstream.URL, "admin@example.com", []int64{7}
						payload.CPA.URL = upstream.URL
						saveBody, _ := json.Marshal(proSettingsInput{ProSettings: payload, Sub2Password: "draft-secret", CPAKey: "draft-secret"})
						saved := httptest.NewRecorder()
						s.saveProSettings(saved, httptest.NewRequest("PUT", "/api/pro-settings", strings.NewReader(string(saveBody))))
						if saved.Code != 200 {
							t.Fatalf("save after reading groups failed: %s", saved.Body.String())
						}
					}
				})
			}
		}
	}
}

func TestProConnectionRejectsMalformedBody(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st}
	for _, body := range []string{`{"sub2":`, `{"sub2":"invalid"}`} {
		out := httptest.NewRecorder()
		_, _, _, ok := s.proConnectionSettings(out, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if ok || out.Code != 400 {
			t.Fatalf("accepted malformed body: %s", body)
		}
	}
}
