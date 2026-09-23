package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func quietProxyLog(string, map[string]any) {}

func TestSub2ProxyConcurrentTeamAndProCreates(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	counts := map[int64]int{1: 0, 2: 0, 3: 8}
	var mu sync.Mutex
	var reading atomic.Bool
	var listCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin"}}`)
		case "/api/v1/admin/proxies/all":
			if reading.Swap(true) {
				t.Error("overlapping count-read/create sequences")
			}
			listCalls.Add(1)
			fmt.Fprintf(w, `{"code":0,"data":[{"id":1,"status":"active","account_count":%d},{"id":2,"status":"active","account_count":%d},{"id":3,"status":"active","account_count":%d}]}`, counts[1], counts[2], counts[3])
		case "/api/v1/admin/accounts":
			var body struct {
				ProxyID int64 `json:"proxy_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !reading.Swap(false) {
				t.Error("create did not reread counts")
			}
			if body.ProxyID != 1 && body.ProxyID != 2 {
				t.Error("selected heavier proxy")
			}
			counts[body.ProxyID]++
			fmt.Fprintf(w, `{"code":0,"data":{"id":%d}}`, counts[1]+counts[2])
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	s := &Server{store: st, sub2: sub2.New()}
	settings := model.Sub2Settings{URL: upstream.URL, Email: "admin@example.test", BindProxy: true, ProxyIDs: []int64{1, 2, 3}}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			scope := fmt.Sprintf("team:%d", i)
			if i%2 == 0 {
				scope = fmt.Sprintf("pro:%d", i)
			}
			config := settings
			if i%2 == 0 {
				config.URL += "/"
			}
			_, err := s.createSub2WithProxy(context.Background(), config, "secret", sub2.CreateAccountInput{Name: scope, GroupIDs: []int64{1}}, scope, "legacy", quietProxyLog)
			if err != nil {
				t.Error(err)
			} else {
				s.completeSub2Push(config, scope)
			}
		}(i)
	}
	wg.Wait()
	if counts[1] != 6 || counts[2] != 6 || counts[3] != 8 || listCalls.Load() != 12 {
		t.Fatalf("counts=%v list=%d", counts, listCalls.Load())
	}
}

func TestSub2ProxyAmbiguousRestartRetainsRequest(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var calls, lists atomic.Int32
	var bodies, keys []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin"}}`)
		case "/api/v1/admin/proxies/all":
			lists.Add(1)
			fmt.Fprint(w, `{"code":0,"data":[{"id":2,"status":"active","account_count":0}]}`)
		case "/api/v1/admin/accounts":
			raw, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(raw))
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			if calls.Add(1) == 1 {
				http.Error(w, "temporary upstream failure", http.StatusBadGateway)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"id":88}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	settings := model.Sub2Settings{URL: upstream.URL, Email: "admin@example.test", BindProxy: true, ProxyIDs: []int64{2}}
	s := &Server{store: st, sub2: sub2.New()}
	input := sub2.CreateAccountInput{Name: "first-name", GroupIDs: []int64{1}, Credentials: map[string]any{"access_token": "sensitive-access-token"}}
	if _, err := s.createSub2WithProxy(context.Background(), settings, "secret", input, "team:a:cycle", "legacy", quietProxyLog); err == nil {
		t.Fatal("expected ambiguous failure")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sensitive-access-token") {
		t.Fatal("pending request stored unencrypted")
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = &Server{store: st, sub2: sub2.New()}
	input.Name = "later-name"
	input.Credentials["access_token"] = "changed-token"
	settings.ProxyIDs = []int64{999}
	account, err := s.createSub2WithProxy(context.Background(), settings, "secret", input, "team:a:cycle", "different-key", quietProxyLog)
	if err != nil || account.ID != 88 {
		t.Fatalf("retry=%+v %v", account, err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || keys[0] != keys[1] || lists.Load() != 1 {
		t.Fatalf("retry changed selection/body/key: lists=%d", lists.Load())
	}
	// A local write failure can replay the successful result without another POST.
	if _, err := s.createSub2WithProxy(context.Background(), settings, "secret", input, "team:a:cycle", "x", quietProxyLog); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("acknowledged create repeated")
	}
	s.completeSub2Push(settings, "team:a:cycle")
	raw, err := st.Sub2PushAttempt(sub2PushAttemptID(upstream.URL, "team:a:cycle"))
	if err != nil || raw != "" {
		t.Fatal("completed attempt not removed")
	}
}

func TestSub2ProxyDisabledAndFailurePaths(t *testing.T) {
	for _, mode := range []string{"off", "empty", "unavailable", "lookup-error", "missing-count"} {
		t.Run(mode, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			lists, creates := 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/login":
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin"}}`)
				case "/api/v1/admin/proxies/all":
					lists++
					if mode == "lookup-error" {
						http.Error(w, "unavailable", 503)
					} else if mode == "missing-count" {
						fmt.Fprint(w, `{"code":0,"data":[{"id":1,"status":"active"}]}`)
					} else {
						fmt.Fprint(w, `{"code":0,"data":[]}`)
					}
				case "/api/v1/admin/accounts":
					creates++
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if _, ok := body["proxy_id"]; ok {
						t.Error("disabled switch sent proxy")
					}
					fmt.Fprint(w, `{"code":0,"data":{"id":1}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			s := &Server{store: st, sub2: sub2.New()}
			settings := model.Sub2Settings{URL: upstream.URL, Email: "admin@example.test", BindProxy: mode != "off", ProxyIDs: []int64{1}}
			if mode == "empty" {
				settings.ProxyIDs = nil
			}
			_, err = s.createSub2WithProxy(context.Background(), settings, "secret", sub2.CreateAccountInput{GroupIDs: []int64{1}}, "test", "key", quietProxyLog)
			if mode == "off" {
				if err != nil || lists != 0 || creates != 1 {
					t.Fatalf("off changed behavior: %v", err)
				}
			} else if err == nil || creates != 0 {
				t.Fatalf("binding failed open: %v creates=%d", err, creates)
			}
		})
	}
}

func TestSub2ProxyLockCancellationAndIndependentServers(t *testing.T) {
	s := &Server{}
	unlock, err := s.lockSub2Push(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.lockSub2Push(ctx, "a"); err == nil {
		t.Fatal("waiting lock ignored cancellation")
	}
	other, err := s.lockSub2Push(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	other()
}

func TestSub2ProxySettingsAndDraftRead(t *testing.T) {
	for _, pro := range []bool{false, true} {
		t.Run(fmt.Sprint(pro), func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/login" {
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin"}}`)
					return
				}
				if r.URL.Path != "/api/v1/admin/proxies/all" || r.URL.Query().Get("with_count") != "true" {
					t.Error("wrong proxy endpoint")
				}
				fmt.Fprint(w, `{"code":0,"data":[{"id":7,"status":"active","account_count":3,"password":"hidden-proxy-password"}]}`)
			}))
			defer upstream.Close()
			team := model.DefaultSub2Settings()
			team.URL = "https://saved.invalid"
			team.Email = "admin@example.test"
			team.BindProxy = true
			team.ProxyIDs = []int64{2, 2, -1}
			_, err = st.SaveSub2Settings(team, "saved-secret")
			if err != nil {
				t.Fatal(err)
			}
			p := model.DefaultProSettings()
			p.Sub2 = team
			p.Sub2.ProxyIDs = []int64{8}
			_, err = st.SaveProSettings(p, "saved-secret", "")
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{store: st, sub2: sub2.New()}
			before, _, _ := st.Sub2Settings()
			beforePro, _, _, _ := st.ProSettings()
			body := fmt.Sprintf(`{"sub2":{"url":%q,"email":"admin@example.test"}}`, upstream.URL)
			r := httptest.NewRequest("POST", "/", strings.NewReader(body))
			w := httptest.NewRecorder()
			if pro {
				s.getProSub2Proxies(w, r)
			} else {
				s.getSub2Proxies(w, r)
			}
			if w.Code != 200 || strings.Contains(w.Body.String(), "hidden-proxy-password") || !strings.Contains(w.Body.String(), `"account_count":3`) {
				t.Fatalf("response: %d %s", w.Code, w.Body)
			}
			after, _, _ := st.Sub2Settings()
			afterPro, _, _, _ := st.ProSettings()
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforePro, afterPro) {
				t.Fatal("draft lookup changed saved settings")
			}
			if !reflect.DeepEqual(after.ProxyIDs, []int64{2}) || !reflect.DeepEqual(afterPro.Sub2.ProxyIDs, []int64{8}) {
				t.Fatal("Team/Pro selection not independent or normalized")
			}
			input := sub2SettingsInput{}
			if err := resolveSub2ProxyInput(&input, after); err != nil || !boolValue(input.BindProxy) || !reflect.DeepEqual(input.ProxyIDs, after.ProxyIDs) {
				t.Fatal("old client omitted fields not retained")
			}
		})
	}
}

func TestSub2ProxyDeadlineRetryRetainsSelection(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var creates, lists atomic.Int32
	var originalBody, originalKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin"}}`)
		case "/api/v1/admin/proxies/all":
			lists.Add(1)
			fmt.Fprint(w, `{"code":0,"data":[{"id":2,"status":"active","account_count":0}]}`)
		case "/api/v1/admin/accounts":
			body, _ := io.ReadAll(r.Body)
			if creates.Add(1) == 1 {
				originalBody, originalKey = string(body), r.Header.Get("Idempotency-Key")
				<-r.Context().Done()
				return
			}
			if string(body) != originalBody || r.Header.Get("Idempotency-Key") != originalKey {
				t.Error("deadline retry changed body/key")
			}
			fmt.Fprint(w, `{"code":0,"data":{"id":99}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	s := &Server{store: st, sub2: sub2.New()}
	settings := model.Sub2Settings{URL: upstream.URL, Email: "admin@example.test", BindProxy: true, ProxyIDs: []int64{2}}
	input := sub2.CreateAccountInput{Name: "first-name", GroupIDs: []int64{1}}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := s.createSub2WithProxy(ctx, settings, "secret", input, "team:timeout", "legacy", quietProxyLog); err == nil || ctx.Err() == nil {
		t.Fatalf("expected request deadline: %v", err)
	}
	if creates.Load() != 1 {
		t.Fatal("timeout did not reach create")
	}
	settings.BindProxy = false // Even a settings change must not duplicate an unconfirmed create.
	input.Name = "later-name"
	if _, err := s.createSub2WithProxy(context.Background(), settings, "secret", input, "team:timeout", "later-key", quietProxyLog); err != nil {
		t.Fatal(err)
	}
	if lists.Load() != 1 || creates.Load() != 2 {
		t.Fatal("deadline retry reselected proxy")
	}
}

func TestSub2ProxySaveEndpoints(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st}
	for _, route := range []string{"team", "legacy", "pro"} {
		for _, enabled := range []bool{true, false} {
			payload := map[string]any{"url": "https://sub.example", "email": "admin@example.test", "password": "secret", "bind_proxy": enabled, "proxy_ids": []int64{11, 12}, "group_ids": []int64{4}}
			var body any = payload
			handler := s.saveSub2Settings
			if route == "team" {
				body = map[string]any{"provider": "sub2", "sub2": payload}
				handler = s.savePushSettings
			}
			if route == "pro" {
				delete(payload, "password")
				body = map[string]any{"provider": "sub2", "sub2": payload, "sub2_password": "secret"}
				handler = s.saveProSettings
			}
			encoded, _ := json.Marshal(body)
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest("PUT", "/", strings.NewReader(string(encoded))))
			if rec.Code != 200 {
				t.Fatalf("%s enabled=%v: %d %s", route, enabled, rec.Code, rec.Body)
			}
			got, _, _ := st.Sub2Settings()
			if route == "pro" {
				p, _, _, _ := st.ProSettings()
				got = p.Sub2
			}
			if got.BindProxy != enabled || !reflect.DeepEqual(got.ProxyIDs, []int64{11, 12}) {
				t.Fatalf("%s save/load lost proxy settings", route)
			}
		}
	}
}
