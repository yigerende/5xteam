package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

type recoveryFixture struct {
	s                                               *Server
	dir                                             string
	admin                                           model.AdminAccountProfile
	mu                                              sync.Mutex
	state                                           map[string]string
	pending                                         map[string]bool
	mutations                                       []string
	bodies                                          []map[string]any
	noSpace, failLeave, ambiguousApprove, holdAgain bool
	onMutation                                      func(*http.Request)
	atStatus, atChecks                              int
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &recoveryFixture{s: &Server{store: st}, dir: dir, state: map[string]string{}, pending: map[string]bool{}}
	t.Cleanup(func() { f.s.stopSeatRecovery(); f.s.store.Close() })
	handler := func(source string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" && f.onMutation != nil {
				f.onMutation(r)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/me") {
				if source != "child" {
					t.Error("AT check must use global proxy")
				}
				if !strings.HasPrefix(token, "eyJ") {
					t.Error("AT check did not use saved token")
				}
				if r.Header.Get("chatgpt-account-id") != "personal" {
					t.Error("AT check used wrong workspace")
				}
				f.atChecks++
				if f.atStatus > 0 && f.atStatus != 200 {
					http.Error(w, "fixture AT check", f.atStatus)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"id": "one", "email": "one@example.com"})
				return
			}
			if source == "mother" && token != "mother-at" {
				t.Errorf("wrong mother credential %q", token)
			}
			if source == "child" && !strings.HasPrefix(token, "child-at-") {
				t.Error("child did not use freshly saved AT")
			}
			if r.Header.Get("chatgpt-account-id") != "team" && r.Header.Get("chatgpt-account-id") != "team-two" {
				t.Error("missing workspace header")
			}
			path := r.URL.Path
			if r.Method == "GET" {
				if source != "mother" {
					t.Error("read used child's proxy")
				}
				switch {
				case strings.HasSuffix(path, "/subscriptions"):
					available := 1
					held, active := 0, 0
					for _, state := range f.state {
						if state == "held" {
							held++
						}
						if state == "prolite" {
							active++
						}
						if state == "default" {
							available = 0
						}
					}
					if f.noSpace {
						available = 0
					}
					if f.holdAgain {
						held++
					}
					json.NewEncoder(w).Encode(map[string]any{"seat_capacity": []any{map[string]any{"type": "default", "paid": 1, "held": 0, "available": available}, map[string]any{"type": "prolite", "paid": len(f.state), "held": held, "available": len(f.state) - held - active}}, "assigned": map[string]int{"default": 1 - available, "prolite": active}})
				case strings.HasSuffix(path, "/users"):
					items := []map[string]any{}
					query := r.URL.Query().Get("query")
					heldOnly := r.URL.Query().Get("active_vacancy_hold_seat_type") == "prolite"
					for name, state := range f.state {
						if query != "" && query != name+"@example.com" {
							continue
						}
						if state == "outside" && !(heldOnly && f.holdAgain) {
							continue
						}
						if heldOnly && state != "held" && !(state == "outside" && f.holdAgain) {
							continue
						}
						item := map[string]any{"id": name, "email": name + "@example.com", "role": "standard-user", "seat_type": state}
						if state == "held" || state == "outside" {
							item["seat_type"] = "prolite"
							item["deactivated_time"] = "2026-09-28T12:00:00Z"
							item["reclaimable_seat_type"] = "prolite"
						}
						items = append(items, item)
					}
					json.NewEncoder(w).Encode(map[string]any{"items": items, "total": len(items)})
				case strings.HasSuffix(path, "/invites"):
					name := strings.TrimSuffix(r.URL.Query().Get("query"), "@example.com")
					items := []map[string]any{}
					if f.pending[name] {
						items = append(items, map[string]any{"id": "req_" + name, "email_address": name + "@example.com"})
					}
					json.NewEncoder(w).Encode(map[string]any{"items": items})
				default:
					t.Errorf("unexpected GET %s", path)
					http.Error(w, "fixture", 404)
				}
				return
			}
			var body map[string]any
			if r.Body != nil {
				_ = json.NewDecoder(r.Body).Decode(&body)
			}
			f.mutations = append(f.mutations, source+":"+r.Method+":"+path)
			f.bodies = append(f.bodies, body)
			name := strings.TrimPrefix(token, "child-at-")
			switch {
			case r.Method == "POST" && strings.HasSuffix(path, "/invites/request"):
				if source != "child" || body != nil {
					t.Error("child request contract")
				}
				f.pending[name] = true
			case r.Method == "POST" && strings.HasSuffix(path, "/invites"):
				if source != "mother" || body["seat_type"] != "prolite" {
					t.Error("invite contract")
				}
				values, _ := body["email_addresses"].([]any)
				name = strings.TrimSuffix(fmt.Sprint(values[0]), "@example.com")
				f.pending[name] = true
			case r.Method == "PATCH" && strings.Contains(path, "/invites/req_"):
				if source != "mother" || body["seat_type"] != "prolite" || body["accept_request"] != true {
					t.Error("approval contract")
				}
				name = strings.Split(path, "req_")[1]
				f.state[name] = "prolite"
				delete(f.pending, name)
				if f.ambiguousApprove {
					http.Error(w, "upstream timed out after approval", 504)
					return
				}
			case r.Method == "POST" && strings.HasSuffix(path, "/invites/accept"):
				if source != "child" {
					t.Error("accept proxy")
				}
				if !f.pending[name] {
					http.Error(w, "missing invite", 404)
					return
				}
				f.state[name] = "prolite"
				delete(f.pending, name)
			case r.Method == "POST" && strings.HasSuffix(path, "/seat/update"):
				parts := strings.Split(path, "/")
				name = parts[len(parts)-3]
				if source != "mother" || body["operation"] != "switch" || body["seat_type"] != "default" || r.Header.Get("Referer") != "https://chatgpt.com/admin/members" {
					t.Error("switch contract")
				}
				for _, key := range []string{"flow_id", "mutation_attempt_id"} {
					if len(fmt.Sprint(body[key])) != 36 {
						t.Errorf("invalid UUID %s", key)
					}
				}
				if f.noSpace {
					t.Error("switched without an available ordinary seat")
				}
				f.state[name] = "default"
			case r.Method == "DELETE":
				parts := strings.Split(path, "/")
				name = parts[len(parts)-1]
				if f.state[name] != "default" {
					t.Error("removed before ordinary seat verified")
				}
				if f.failLeave {
					http.Error(w, "fixture 429", 429)
					return
				}
				f.state[name] = "outside"
			default:
				t.Errorf("unexpected mutation %s %s", r.Method, path)
			}
			json.NewEncoder(w).Encode(map[string]any{"success": true})
		}
	}
	mother := httptest.NewServer(handler("mother"))
	child := httptest.NewServer(handler("child"))
	t.Cleanup(mother.Close)
	t.Cleanup(child.Close)
	settings := model.DefaultSettings()
	settings.BaseURL = "http://127.0.0.1:1/backend-api"
	settings.ProxyURL = child.URL
	settings.NetworkRetryCount = 0
	if err = st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	rotation := model.DefaultAutoRotationSettings()
	rotation.TeamOperationIntervalSeconds = 0
	if _, err = st.SaveAutoRotationSettings(rotation); err != nil {
		t.Fatal(err)
	}
	f.admin = saveTestAdmin(t, st, model.AdminAccountProfile{Label: "mother", Email: "mother@example.com", TeamAccountID: "team", RotationDisabled: true}, "mother-at", mother.URL)
	f.setLogin()
	return f
}

func (f *recoveryFixture) setLogin() {
	f.s.recovery.login = func(email string, progress func(string), diagnostic func(protocolOAuthDiagnostic)) (map[string]any, error) {
		progress("fixture login completed")
		return map[string]any{"success": true, "access_token": "child-at-" + strings.TrimSuffix(email, "@example.com"), "session_token": "secret-session"}, nil
	}
}
func (f *recoveryFixture) task(t *testing.T, name, join, leave string) model.SeatRecoveryTask {
	return f.taskFromMailState(t, name, join, leave, "used")
}
func (f *recoveryFixture) taskFromMailState(t *testing.T, name, join, leave, state string) model.SeatRecoveryTask {
	t.Helper()
	email := name + "@example.com"
	f.mu.Lock()
	f.state[name] = "held"
	f.mu.Unlock()
	_, err := f.s.store.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "old-at", RefreshToken: "preserved-rt"})
	if err != nil {
		t.Fatal(err)
	}
	if state != "mail-only" {
		p, _, err := f.s.store.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: name}, "old-source-at")
		if err != nil {
			t.Fatal(err)
		}
		if state != "waiting-first" {
			p, err = f.s.store.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
				p.TeamAccountID = "team"
				p.AcceptStatus = "completed"
				p.RemoveStatus = "completed"
				p.DownstreamCleaned = true
			})
			if err != nil {
				t.Fatal(err)
			}
			if state == "waiting-reuse" {
				if _, err = f.s.store.BeginFreeAccountCycle(p.ID, p.CycleID, "old-source-at"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	settings := model.DefaultSeatRecoverySettings()
	settings.JoinMethod = join
	settings.RemoveMethod = leave
	settings.DwellMinutes = 0
	settings.OperationIntervalSeconds = 0
	settings.NextIntervalSeconds = 0
	if err = f.s.store.SaveSeatRecoverySettings(settings); err != nil {
		t.Fatal(err)
	}
	if state != "used" {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("id", f.admin.ID)
		w := httptest.NewRecorder()
		f.s.scanSeatRecovery(w, r)
		var scanned struct {
			Data struct {
				Items []recoveryCandidate `json:"items"`
			} `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &scanned) != nil || len(scanned.Data.Items) != 1 || !scanned.Data.Items[0].Eligible {
			t.Fatalf("outside account did not match scan: %d %s", w.Code, w.Body.String())
		}
	}
	body, _ := json.Marshal(map[string]any{"emails": []string{email}, "team_id": "team"})
	r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	r.SetPathValue("id", f.admin.ID)
	w := httptest.NewRecorder()
	f.s.startSeatRecovery(w, r)
	if w.Code != 202 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Items    []model.SeatRecoveryTask `json:"items"`
			Failures map[string]string        `json:"failures"`
		} `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Data.Items) != 1 {
		t.Fatalf("start body %s", w.Body.String())
	}
	return response.Data.Items[0]
}
func (f *recoveryFixture) get(t *testing.T, id string) model.SeatRecoveryTask {
	t.Helper()
	p, e := f.s.store.SeatRecoveryTask(id)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func (f *recoveryFixture) runUntil(t *testing.T, id, stage string) model.SeatRecoveryTask {
	t.Helper()
	for i := 0; i < 35; i++ {
		p := f.get(t, id)
		if p.Stage == stage {
			return p
		}
		if p.Status == "failed" {
			t.Fatalf("failed at %s: %s", p.Stage, p.Message)
		}
		f.s.runSeatRecoveryStep(t.Context(), id)
	}
	t.Fatalf("did not reach %s: %+v", stage, f.get(t, id))
	return model.SeatRecoveryTask{}
}
func (f *recoveryFixture) retry(t *testing.T, id string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"action":"retry"}`))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	f.s.controlSeatRecovery(w, r)
	if w.Code != 200 {
		t.Fatalf("retry %s", w.Body.String())
	}
}

func TestSeatRecoveryFourCombinationsLeaveTeamUntouched(t *testing.T) {
	for _, join := range []string{"mother_invite", "child_request"} {
		for _, leave := range []string{"mother_kick", "child_leave"} {
			t.Run(join+"/"+leave, func(t *testing.T) {
				f := newRecoveryFixture(t)
				p := f.task(t, "one", join, leave)
				before, _, _ := f.s.store.FreeAccountCredential(p.FreeID)
				rotation := f.s.store.AutoRotationSettings()
				admins := f.s.store.AdminAccounts()
				visits, _ := f.s.store.TeamVisits(p.Email, p.UserID)
				done := f.runUntil(t, p.ID, "completed")
				if !done.Finished || done.Lane || done.JoinedAt == nil || done.LeftAt == nil {
					t.Fatalf("bad completion %+v", done)
				}
				after, _, _ := f.s.store.FreeAccountCredential(p.FreeID)
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(rotation, f.s.store.AutoRotationSettings()) || !reflect.DeepEqual(admins, f.s.store.AdminAccounts()) {
					t.Fatal("Team profile/settings/admin changed")
				}
				newVisits, _ := f.s.store.TeamVisits(p.Email, p.UserID)
				if !reflect.DeepEqual(visits, newVisits) {
					t.Fatal("Team history changed")
				}
				mail, c, err := f.s.store.MailAccountCredential(p.Email)
				if err != nil || c.AccessToken != "child-at-one" || c.RefreshToken != "preserved-rt" || !strings.Contains(c.ChatGPTSession, "secret-session") || !mail.ATValid {
					t.Fatalf("login not persisted safely: %v", err)
				}
				logs, _ := f.s.store.SeatRecoveryLogs(p.ID, 0)
				encoded, _ := json.Marshal(logs)
				if strings.Contains(string(encoded), "secret-session") || strings.Contains(string(encoded), "preserved-rt") {
					t.Fatal("credential leaked in logs")
				}
				if len(f.mutations) != 4 {
					t.Fatalf("mutations %v", f.mutations)
				}
				last := f.mutations[len(f.mutations)-1]
				if (leave == "child_leave") != strings.HasPrefix(last, "child:") {
					t.Fatalf("wrong removal principal %s", last)
				}
			})
		}
	}
}

func TestSeatRecoveryAmbiguousApprovalAndCrashReconcile(t *testing.T) {
	f := newRecoveryFixture(t)
	f.ambiguousApprove = true
	p := f.task(t, "one", "child_request", "mother_kick")
	f.runUntil(t, p.ID, "dwell")
	if len(f.mutations) != 2 {
		t.Fatal("approval resent after successful 504")
	}
	f.runUntil(t, p.ID, "switch")
	f.s.runSeatRecoveryStep(t.Context(), p.ID)
	if f.get(t, p.ID).Stage != "switch" || f.state["one"] != "default" {
		t.Fatal("fixture did not interrupt after switch")
	}
	if err := f.s.store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.s = &Server{store: st}
	f.setLogin()
	f.runUntil(t, p.ID, "completed")
	if len(f.mutations) != 4 {
		t.Fatalf("crash duplicated switch: %v", f.mutations)
	}
}

func TestSeatRecoveryOrdinaryLaneSurvivesFailurePauseAndRetry(t *testing.T) {
	f := newRecoveryFixture(t)
	a := f.task(t, "one", "mother_invite", "child_leave")
	b := f.task(t, "two", "child_request", "mother_kick")
	f.runUntil(t, a.ID, "switch")
	f.runUntil(t, b.ID, "switch")
	f.noSpace = true
	f.s.runSeatRecoveryStep(t.Context(), a.ID)
	p := f.get(t, a.ID)
	if p.Status != "waiting" || !p.Lane || f.state["one"] != "prolite" {
		t.Fatal("ordinary capacity gate failed")
	}
	f.noSpace = false
	f.runUntil(t, a.ID, "leave")
	f.failLeave = true
	for i := 0; i < 14; i++ {
		f.s.runSeatRecoveryStep(t.Context(), a.ID)
	}
	if p = f.get(t, a.ID); p.Status != "failed" || !p.Lane {
		t.Fatal("failed cleanup released lane")
	}
	f.s.runSeatRecoveryStep(t.Context(), b.ID)
	if f.state["two"] != "prolite" || !strings.Contains(f.get(t, b.ID).Message, "上一子号") {
		t.Fatal("second child stole ordinary slot")
	}
	f.failLeave = false
	f.retry(t, a.ID)
	f.runUntil(t, a.ID, "verify")
	f.s.store.UpdateSeatRecoveryTask(a.ID, func(p *model.SeatRecoveryTask) { p.Paused = true; p.Settings.NextIntervalSeconds = 10 })
	f.s.runSeatRecoveryStep(t.Context(), a.ID)
	if p = f.get(t, a.ID); p.Stage != "cooldown" || !p.Lane || !p.Paused {
		t.Fatal("paused lane did not drain")
	}
	f.s.runSeatRecoveryStep(t.Context(), b.ID)
	if f.state["two"] != "prolite" {
		t.Fatal("cooldown bypassed")
	}
	f.s.store.UpdateSeatRecoveryTask(a.ID, func(p *model.SeatRecoveryTask) { p.NextAt = time.Now().Add(-time.Second) })
	f.runUntil(t, a.ID, "completed")
	f.runUntil(t, b.ID, "completed")
}

func TestSeatRecoveryCannotPretendHeldWasRestored(t *testing.T) {
	f := newRecoveryFixture(t)
	p := f.task(t, "one", "mother_invite", "mother_kick")
	f.runUntil(t, p.ID, "verify")
	f.holdAgain = true
	for i := 0; i < 14; i++ {
		f.s.runSeatRecoveryStep(t.Context(), p.ID)
	}
	p = f.get(t, p.ID)
	if p.Status != "failed" || p.Finished || !p.Lane {
		t.Fatal("reported success while 5x still held")
	}
}

func TestSeatRecoveryRejectsRotationRequeueAndSharesMotherLock(t *testing.T) {
	f := newRecoveryFixture(t)
	p := f.task(t, "one", "child_request", "mother_kick")
	f.runUntil(t, p.ID, "join")
	unlock := f.s.lockTeamAccountRemove("team")
	f.s.runSeatRecoveryStep(t.Context(), p.ID)
	unlock()
	if len(f.mutations) != 0 || !strings.Contains(f.get(t, p.ID).Message, "串行") {
		t.Fatal("bypassed Team mother lock")
	}
	f.s.store.UpdateFreeAccount(p.FreeID, func(p *model.FreeAccountProfile) { p.ReusePending = true; p.RemoveStatus = "pending" })
	f.s.runSeatRecoveryStep(t.Context(), p.ID)
	if len(f.mutations) != 0 || f.get(t, p.ID).Status != "failed" {
		t.Fatal("stole requeued Team child")
	}
}

func TestSeatRecoveryDispatchBoundsConcurrencyAndPause(t *testing.T) {
	f := newRecoveryFixture(t)
	var tasks []model.SeatRecoveryTask
	for _, name := range []string{"one", "two", "three", "four"} {
		tasks = append(tasks, f.task(t, name, "mother_invite", "mother_kick"))
	}
	entered := make(chan string, 4)
	release := make(chan struct{})
	defer close(release)
	f.s.recovery.active = map[string]bool{}
	f.s.recovery.login = func(email string, _ func(string), _ func(protocolOAuthDiagnostic)) (map[string]any, error) {
		entered <- email
		<-release
		return map[string]any{"success": true, "access_token": "child-at-" + strings.TrimSuffix(email, "@example.com")}, nil
	}
	f.s.store.UpdateSeatRecoveryTask(tasks[0].ID, func(p *model.SeatRecoveryTask) { p.Paused = true })
	f.s.dispatchSeatRecovery(context.Background())
	for i := 0; i < 2; i++ {
		select {
		case email := <-entered:
			if email == tasks[0].Email {
				t.Fatal("paused account logged in")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("workers not started")
		}
	}
	select {
	case <-entered:
		t.Fatal("exceeded configured login concurrency")
	case <-time.After(30 * time.Millisecond):
	}
	// Release before cleanup waits for the worker group.
}

func TestSeatRecoverySettingsAndDuplicateAdmission(t *testing.T) {
	f := newRecoveryFixture(t)
	p := f.task(t, "one", "mother_invite", "mother_kick")
	dup := p
	dup.ID = recoveryID()
	if err := f.s.store.CreateSeatRecoveryTask(dup); err == nil {
		t.Fatal("duplicate child task admitted")
	}
	changed := model.DefaultSeatRecoverySettings()
	changed.JoinMethod = "child_request"
	changed.DwellMinutes = 20
	f.s.store.SaveSeatRecoverySettings(changed)
	if got := f.get(t, p.ID); got.Settings != p.Settings {
		t.Fatal("in-flight config changed")
	}
	for _, bad := range []model.SeatRecoverySettings{{}, {Concurrency: 99}} {
		if validateRecoverySettings(bad) == nil {
			t.Fatal("invalid settings accepted")
		}
	}
	for _, n := range []int{9, 100, 10000} {
		valid := model.DefaultSeatRecoverySettings()
		valid.Concurrency = n
		if err := validateRecoverySettings(valid); err != nil {
			t.Fatalf("unnecessary concurrency ceiling: %d %v", n, err)
		}
	}
}

func recoveryAT(name string, expired bool) string {
	exp := time.Now().Add(time.Hour).Unix()
	if expired {
		exp = time.Now().Add(-time.Hour).Unix()
	}
	b, _ := json.Marshal(map[string]any{"exp": exp, "https://api.openai.com/auth": map[string]string{"chatgpt_user_id": name, "chatgpt_account_id": "personal"}, "https://api.openai.com/profile": map[string]string{"email": name + "@example.com"}})
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(b) + ".fixture"
}

func TestSeatRecoveryATReuseAndLoginClassification(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		token             string
		wantLogin, failed bool
		checks            int
	}{
		{"valid", 200, recoveryAT("one", false), false, false, 1},
		{"401", 401, recoveryAT("one", false), true, false, 1},
		{"403", 403, recoveryAT("one", false), false, true, 1},
		{"429", 429, recoveryAT("one", false), false, true, 1},
		{"500", 500, recoveryAT("one", false), false, true, 1},
		{"expired", 200, recoveryAT("one", true), true, false, 0},
		{"absent", 200, "", true, false, 0},
		{"malformed", 200, "not-a-jwt", true, false, 0},
		{"wrong-identity", 200, recoveryAT("other", false), false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			p := f.task(t, "one", "mother_invite", "mother_kick")
			f.atStatus = tc.status
			mail, c, err := f.s.store.MailAccountCredential(p.Email)
			if err != nil {
				t.Fatal(err)
			}
			c.AccessToken = tc.token
			c.ChatGPTSession = `{"session":"original"}`
			if _, err = f.s.store.SaveMailAccount(mail, c); err != nil {
				t.Fatal(err)
			}
			called := false
			original := f.s.recovery.login
			f.s.recovery.login = func(email string, progress func(string), diagnostic func(protocolOAuthDiagnostic)) (map[string]any, error) {
				called = true
				return original(email, progress, diagnostic)
			}
			f.s.runSeatRecoveryStep(t.Context(), p.ID)
			got := f.get(t, p.ID)
			if (got.Status == "failed") != tc.failed || called != tc.wantLogin || f.atChecks != tc.checks {
				t.Fatalf("result %+v,login=%v,checks=%d", got, called, f.atChecks)
			}
			mail, c, err = f.s.store.MailAccountCredential(p.Email)
			if err != nil {
				t.Fatal(err)
			}
			if !called && (c.AccessToken != tc.token || c.ChatGPTSession != `{"session":"original"}` || c.RefreshToken != "preserved-rt") {
				t.Fatal("reusing AT overwrote credentials")
			}
			if !tc.failed && !mail.ATValid {
				t.Fatal("successful check/login did not persist AT validity")
			}
			if !tc.failed && got.Stage != "join" {
				t.Fatal("not ready for serial entry")
			}
		})
	}
}

func TestSeatRecoveryWholeEntrySerialAndRestart(t *testing.T) {
	for _, join := range []string{"mother_invite", "child_request"} {
		t.Run(join, func(t *testing.T) {
			f := newRecoveryFixture(t)
			a := f.task(t, "one", join, "mother_kick")
			b := f.task(t, "two", join, "mother_kick")
			f.runUntil(t, a.ID, "join")
			f.runUntil(t, b.ID, "join")
			f.s.runSeatRecoveryStep(t.Context(), a.ID) // First request sent, not yet approved.
			f.s.runSeatRecoveryStep(t.Context(), b.ID)
			if len(f.mutations) != 1 || f.get(t, b.ID).JoinSent {
				t.Fatal("second child requested before first approval")
			}
			if err := f.s.store.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			f.s = &Server{store: st}
			f.setLogin()
			f.s.runSeatRecoveryStep(t.Context(), b.ID)
			if len(f.mutations) != 1 {
				t.Fatal("restart lost entry lane")
			}
			f.s.runSeatRecoveryStep(t.Context(), a.ID) // Approval HTTP done; membership not verified yet.
			f.s.runSeatRecoveryStep(t.Context(), b.ID)
			if len(f.mutations) != 2 {
				t.Fatal("entry released before membership verification")
			}
			f.runUntil(t, a.ID, "dwell")
			f.s.runSeatRecoveryStep(t.Context(), b.ID)
			if len(f.mutations) != 3 || !f.get(t, b.ID).JoinSent {
				t.Fatal("dwell blocked next recall")
			}
		})
	}
}

func TestSeatRecoveryATConcurrencyAboveEight(t *testing.T) {
	f := newRecoveryFixture(t)
	for i := 0; i < 12; i++ {
		p := f.task(t, fmt.Sprintf("child%d", i), "mother_invite", "mother_kick")
		if _, err := f.s.store.UpdateSeatRecoveryTask(p.ID, func(v *model.SeatRecoveryTask) { v.Settings.Concurrency = 100 }); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{}, 12)
	release := make(chan struct{})
	defer close(release)
	f.s.recovery.login = func(email string, _ func(string), _ func(protocolOAuthDiagnostic)) (map[string]any, error) {
		entered <- struct{}{}
		<-release
		return map[string]any{"success": true, "access_token": "child-at-" + strings.TrimSuffix(email, "@example.com")}, nil
	}
	f.s.dispatchSeatRecovery(t.Context())
	for i := 0; i < 12; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d preparations started", i)
		}
	}
}

func TestSeatRecoveryLoginDoesNotBlockReadyEntry(t *testing.T) {
	f := newRecoveryFixture(t)
	slow := f.task(t, "slow", "mother_invite", "mother_kick")
	ready := f.task(t, "ready", "mother_invite", "mother_kick")
	f.runUntil(t, ready.ID, "join")
	for _, p := range []model.SeatRecoveryTask{slow, ready} {
		if _, err := f.s.store.UpdateSeatRecoveryTask(p.ID, func(v *model.SeatRecoveryTask) { v.Settings.Concurrency = 1 }); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	f.s.recovery.login = func(email string, _ func(string), _ func(protocolOAuthDiagnostic)) (map[string]any, error) {
		close(entered)
		<-release
		return map[string]any{"success": true, "access_token": "child-at-slow"}, nil
	}
	f.s.dispatchSeatRecovery(t.Context())
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("login not started")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.get(t, ready.ID).Stage == "confirm" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("occupied AT concurrency blocked an already-ready recall")
}

func TestSeatRecoveryMotherMutationsSerialButDifferentMothersParallel(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(fmt.Sprint(different), func(t *testing.T) {
			f := newRecoveryFixture(t)
			a := f.task(t, "one", "mother_invite", "mother_kick")
			b := f.task(t, "two", "mother_invite", "mother_kick")
			f.runUntil(t, a.ID, "join")
			f.runUntil(t, b.ID, "join")
			if different {
				other, err := f.s.store.SaveAdminAccount(model.AdminAccountProfile{Label: "other", Email: "other@example.com", TeamAccountID: "team-two", ProxyID: f.admin.ProxyID}, "mother-at")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.s.store.UpdateSeatRecoveryTask(b.ID, func(p *model.SeatRecoveryTask) { p.AdminID = other.ID; p.TeamID = other.TeamAccountID }); err != nil {
					t.Fatal(err)
				}
			}
			entered := make(chan string, 2)
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			f.onMutation = func(r *http.Request) { entered <- r.Header.Get("chatgpt-account-id"); <-release }
			done := make(chan struct{}, 2)
			go func() { f.s.runSeatRecoveryStep(t.Context(), a.ID); done <- struct{}{} }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first request did not enter")
			}
			go func() { f.s.runSeatRecoveryStep(t.Context(), b.ID); done <- struct{}{} }()
			if different {
				select {
				case team := <-entered:
					if team != "team-two" {
						t.Fatal("wrong second team")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("different mother was blocked")
				}
			} else {
				select {
				case <-entered:
					t.Fatal("same mother mutated concurrently")
				case <-done:
				}
				if !strings.Contains(f.get(t, b.ID).Message, "串行") {
					t.Fatal("same mother did not queue")
				}
			}
			once.Do(func() { close(release) })
			remaining := 1
			if different {
				remaining = 2
			}
			for i := 0; i < remaining; i++ {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("worker not released")
				}
			}
		})
	}
}

func TestSeatRecoveryUnusedAndRequeuedAccountsComplete(t *testing.T) {
	for _, state := range []string{"mail-only", "waiting-first", "waiting-reuse"} {
		for _, join := range []string{"mother_invite", "child_request"} {
			t.Run(state+"/"+join, func(t *testing.T) {
				f := newRecoveryFixture(t)
				p := f.taskFromMailState(t, "one", join, "mother_kick", state)
				before, err := f.s.store.FreeAccountForEmail(p.Email)
				if err != nil {
					t.Fatal(err)
				}
				f.runUntil(t, p.ID, "completed")
				after, err := f.s.store.FreeAccountForEmail(p.Email)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("recovery modified waiting Team pipeline")
				}
			})
		}
	}
}

func TestSeatRecoveryUnusedReservationBlocksManualJoin(t *testing.T) {
	f := newRecoveryFixture(t)
	p := f.taskFromMailState(t, "one", "mother_invite", "mother_kick", "waiting-first")
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"admin_account_id":"`+f.admin.ID+`","seat_type":"prolite"}`))
	r.SetPathValue("id", p.FreeID)
	w := httptest.NewRecorder()
	f.s.joinFreeAccount(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "席位恢复") || len(f.mutations) != 0 {
		t.Fatalf("manual Team join bypassed recovery reservation: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.s.store.UpdateSeatRecoveryTask(p.ID, func(p *model.SeatRecoveryTask) { p.Finished = true }); err != nil {
		t.Fatal(err)
	}
	unlock := f.s.lockFreeAccount(p.FreeID)
	err := f.s.createSeatRecoveryTask(p)
	unlock()
	if err == nil || !strings.Contains(err.Error(), "正在执行其他操作") {
		t.Fatal("recovery admission ignored manual account lock")
	}
}
