package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

type livePushRemote struct {
	url    string
	status int
	bodies []string
	keys   []string
	lists  int
}

func newLivePushRemote(t *testing.T) *livePushRemote {
	t.Helper()
	f := &livePushRemote{}
	r := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture-admin"}}`)
		case "/api/v1/admin/proxies/all":
			f.lists++
			fmt.Fprint(w, `{"code":0,"data":[{"id":26,"status":"active","account_count":3},{"id":35,"status":"active","account_count":0},{"id":25,"status":"active","account_count":0}]}`)
		case "/api/v1/admin/accounts":
			if r.Method != http.MethodPost {
				t.Errorf("unexpected method: %s", r.Method)
			}
			raw, _ := io.ReadAll(r.Body)
			f.bodies = append(f.bodies, string(raw))
			f.keys = append(f.keys, r.Header.Get("Idempotency-Key"))
			if f.status != 0 {
				http.Error(w, "fixture rejection", f.status)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"id":88,"name":"fixture-created"}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	f.url = r.URL
	t.Cleanup(r.Close)
	return f
}

func livePushFixture(t *testing.T, remote *livePushRemote, bind bool) (*Server, string, string, model.ProSettings) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, sub2: sub2.New(), auditQueue: make(chan model.AutoRotationEvent, 100)}
	t.Cleanup(func() { _ = s.store.Close() })
	v := model.DefaultProSettings()
	v.Sub2.URL, v.Sub2.Email, v.Sub2.GroupIDs = remote.url, "admin@example.test", []int64{1}
	v.Sub2.BindProxy, v.Sub2.ProxyIDs = bind, []int64{25}
	v, err = st.SaveProSettings(v, "fixture-password", "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-restock", Provider: "sub2", Sub2: v.Sub2, Secret: "fixture-password"})
	if err != nil {
		t.Fatal(err)
	}
	email := "push@example.test"
	_, err = st.SaveMailAccount(model.MailAccountProfile{Email: email, ManagementScope: "pro"}, model.MailAccountCredentials{Email: email, AccessToken: proAutoToken(email, "fixture"), RefreshToken: "fixture-rt"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProRestockTargetID = target
		p.ProAuto = model.ProAutoState{ID: "queued", Status: "interrupted", Stage: "push", RestockBinding: restockBinding(v), Sub2PushGroupIDs: []int64{1, 15}}
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, email, dir, v
}

func TestProPushUsesLatestSavedSettings(t *testing.T) {
	for _, tc := range []struct {
		name             string
		oldBind, newBind bool
		ids              []int64
		proxy            float64
	}{
		{"enable_after_queue", false, true, []int64{26, 35}, 35},
		{"replace_selection", true, true, []int64{26}, 26},
		{"disable_after_queue", true, false, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := newLivePushRemote(t)
			s, email, _, v := livePushFixture(t, remote, tc.oldBind)
			v.Sub2.BindProxy, v.Sub2.ProxyIDs = tc.newBind, tc.ids
			v.Sub2.GroupIDs, v.Sub2.Models = []int64{99}, []string{"new-model"}
			v.Sub2.Priority, v.Sub2.AccountConcurrency, v.Sub2.CpaWS = 7, 23, true
			if _, err := s.store.SaveProSettings(v, "", ""); err != nil {
				t.Fatal(err)
			}
			p, err := s.performProPush(context.Background(), email)
			if err != nil {
				t.Fatal(err)
			}
			if len(remote.bodies) != 1 {
				t.Fatal("expected one create", len(remote.bodies))
			}
			var body map[string]any
			if err = json.Unmarshal([]byte(remote.bodies[0]), &body); err != nil {
				t.Fatal(err)
			}
			if tc.newBind && body["proxy_id"] != tc.proxy {
				t.Fatal("new proxy selection ignored", body["proxy_id"])
			}
			if !tc.newBind && (body["proxy_id"] != nil || remote.lists != 0) {
				t.Fatal("binding was disabled")
			}
			if !reflect.DeepEqual(body["group_ids"], []any{float64(99), float64(15)}) || body["concurrency"] != float64(23) || body["priority"] != float64(7) || body["cpa_ws"] != float64(1) {
				t.Fatal("new push fields ignored")
			}
			creds := body["credentials"].(map[string]any)
			if !reflect.DeepEqual(creds["model_mapping"], map[string]any{"new-model": "new-model"}) {
				t.Fatal("new models ignored")
			}
			if p.ProAuto.RestockBinding != restockBinding(v) {
				t.Fatal("restock retained old binding")
			}
			v.Sub2.URL = "https://later.example.test"
			if _, err = s.store.SaveProSettings(v, "later", ""); err != nil {
				t.Fatal(err)
			}
			actual, _, _, err := s.proAccountPushSettings(p)
			if err != nil || actual.Sub2.URL != remote.url || actual.Sub2.BindProxy != tc.newBind || !slices.Equal(actual.Sub2.ProxyIDs, tc.ids) {
				t.Fatal("actual downstream not retained", err)
			}
		})
	}
}

func TestProPushPendingRetryKeepsSiteAndExactRequestAfterRestart(t *testing.T) {
	for _, bind := range []bool{false, true} {
		t.Run(fmt.Sprint(bind), func(t *testing.T) {
			remote := newLivePushRemote(t)
			s, email, dir, v := livePushFixture(t, remote, bind)
			remote.status = http.StatusBadGateway
			if _, err := s.performProPush(context.Background(), email); err == nil {
				t.Fatal("expected failure")
			}
			other := newLivePushRemote(t)
			v.Sub2.URL, v.Sub2.GroupIDs, v.Sub2.ProxyIDs = other.url, []int64{99}, []int64{999}
			if _, err := s.store.SaveProSettings(v, "new-password", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.store.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			s.store, err = store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			s.sub2 = sub2.New()
			remote.status = 0
			p, err := s.performProPush(context.Background(), email)
			if err != nil || p.Sub2AccountID != 88 {
				t.Fatal("retry failed", err)
			}
			if len(other.bodies) != 0 || len(remote.bodies) != 2 || remote.bodies[0] != remote.bodies[1] || remote.keys[0] != remote.keys[1] || remote.keys[0] == "" {
				t.Fatal("pending retry changed request/site/key")
			}
			if (bind && remote.lists != 1) || (!bind && remote.lists != 0) {
				t.Fatal("retry selected proxy again")
			}
		})
	}
}

func TestProPushUnsubmittedFailureCanUseNewConfig(t *testing.T) {
	for _, failure := range []string{"proxy_unavailable", "explicit_rejection"} {
		t.Run(failure, func(t *testing.T) {
			remote := newLivePushRemote(t)
			s, email, _, v := livePushFixture(t, remote, true)
			if failure == "proxy_unavailable" {
				v.Sub2.ProxyIDs = []int64{999}
			} else {
				remote.status = http.StatusBadRequest
			}
			if _, err := s.store.SaveProSettings(v, "", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := s.performProPush(context.Background(), email); err == nil {
				t.Fatal("expected failure")
			}
			other := newLivePushRemote(t)
			v.Sub2.URL, v.Sub2.ProxyIDs, v.Sub2.GroupIDs = other.url, []int64{35}, []int64{99}
			if _, err := s.store.SaveProSettings(v, "new-password", ""); err != nil {
				t.Fatal(err)
			}
			p, err := s.performProPush(context.Background(), email)
			if err != nil || p.Sub2AccountID != 88 || len(other.bodies) != 1 {
				t.Fatal("new config not applied after rejection", err)
			}
			var body struct {
				Groups  []int64 `json:"group_ids"`
				ProxyID int64   `json:"proxy_id"`
			}
			if err = json.Unmarshal([]byte(other.bodies[0]), &body); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(body.Groups, []int64{99}) || body.ProxyID != 35 {
				t.Fatal("old site groups/proxy leaked", body)
			}
		})
	}
}

func TestProPushRestockVerifiesActualNewGroups(t *testing.T) {
	remote := newLivePushRemote(t)
	s, email, dir, v := livePushFixture(t, remote, false)
	oldBinding := restockBinding(v)
	v.Sub2.GroupIDs = []int64{99}
	if _, err := s.store.SaveProSettings(v, "", ""); err != nil {
		t.Fatal(err)
	}
	p, err := s.performProPush(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProRestockTaskID = "delivery"
		p.ProAuto.Status, p.ProAuto.Stage = "waiting_quota", "quota"
		p.ProAuto.Steps = map[string]string{"oauth": "completed", "push": "completed"}
		p.ProAuto.StageTimes = map[string]time.Time{"push:completed": now.Add(-time.Second)}
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := restock.Task{ID: "delivery", Email: email, Status: "running", Binding: oldBinding, GroupID: 1, CreatedAt: now, SubmittedAt: &now}
	raw, _ := json.Marshal(task)
	if _, err = db.Exec(`INSERT INTO pro_restock_tasks VALUES(?,?,?,?,?,?)`, task.ID, email, task.Status, now.Format(time.RFC3339Nano), 0, string(raw)); err != nil {
		t.Fatal(err)
	}
	c := restock.DefaultConfig()
	c.GroupID = 99
	snap := restock.Snapshot{ID: "new", At: now, CollectionStartedAt: now, Valid: true, LoadValid: true, Binding: restockBinding(v), GroupID: 99, Accounts: []restock.Account{{ID: p.Sub2AccountID, Ready: true, Capacity: 50}}}
	tasks, _, err := s.restockTasks(snap, v, c)
	if err != nil || len(tasks) != 1 {
		t.Fatal("task read failed", err)
	}
	if tasks[0].Status != "ready" || tasks[0].GroupID != 99 || tasks[0].Binding != snap.Binding {
		t.Fatal("task still verifies old groups", tasks[0].Status, tasks[0].GroupID)
	}
}

func TestProPushLatestAuthModeUsesMatchingCredentials(t *testing.T) {
	for _, mode := range []string{"siwc", ""} {
		t.Run("current_"+mode, func(t *testing.T) {
			remote := newLivePushRemote(t)
			s, email, _, v := livePushFixture(t, remote, false)
			if mode == "" {
				old := v
				old.Sub2.AuthMode = "siwc"
				id, err := s.store.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro-restock", Provider: "sub2", Sub2: old.Sub2, Secret: "fixture-password"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.store.UpdateProAccount(email, func(p *model.MailAccountProfile) { p.ProRestockTargetID = id }); err != nil {
					t.Fatal(err)
				}
			}
			tokens := model.SIWCCredentials{AuthMode: "siwc", Email: email, AccessToken: "siwc-at", RefreshToken: "siwc-rt", IDToken: "siwc-id", ClientID: "app", ExtAgentHostID: "host", Subject: "subject", ExpiresAt: time.Now().Add(time.Hour).Unix(), WebAccessToken: proAutoToken(email, "siwc-web")}
			if err := s.store.SaveMailAccountSIWC(email, tokens); err != nil {
				t.Fatal(err)
			}
			v.Sub2.AuthMode, v.Sub2.BindProxy, v.Sub2.ProxyIDs = mode, true, []int64{35}
			if _, err := s.store.SaveProSettings(v, "fixture-password", ""); err != nil {
				t.Fatal(err)
			}
			p, err := s.performProPush(context.Background(), email)
			if err != nil || p.Sub2AccountID != 88 {
				t.Fatal("push failed", err)
			}
			var body struct {
				ProxyID     int64          `json:"proxy_id"`
				Credentials map[string]any `json:"credentials"`
			}
			if err = json.Unmarshal([]byte(remote.bodies[0]), &body); err != nil {
				t.Fatal(err)
			}
			if body.ProxyID != 35 {
				t.Fatal("SIWC selection ignored")
			}
			if mode == "siwc" {
				if body.Credentials["auth_mode"] != "siwc" || body.Credentials["access_token"] != "siwc-at" {
					t.Fatal("missing SIWC credentials")
				}
			} else if body.Credentials["auth_mode"] == "siwc" || body.Credentials["refresh_token"] != "fixture-rt" {
				t.Fatal("old SIWC mode replaced OAuth credentials")
			}
		})
	}
}
