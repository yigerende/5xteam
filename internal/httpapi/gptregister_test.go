package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptregister"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

type registerFake struct {
	mu               sync.Mutex
	quantity         int
	oauth            bool
	status           string
	delivered        int
	creates          int
	requests         int
	key              string
	loseCreate       bool
	closed           bool
	credentialStatus int
	meStatus         int
	user             string
}

func (f *registerFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if r.Header.Get("Authorization") != "Bearer fixture-key" {
		w.WriteHeader(401)
		return
	}
	order := func() gptregister.Order {
		return gptregister.Order{ID: "remote-order", Quantity: f.quantity, NeedOAuth: f.oauth, Status: f.status, Delivered: f.delivered, UnitPrice: 7, Charged: int64(f.delivered * 7)}
	}
	var value any
	switch {
	case r.URL.Path == "/open/v1/me":
		if f.meStatus != 0 {
			w.Header().Set("Retry-After", "90")
			w.WriteHeader(f.meStatus)
			w.Write([]byte(`{"error":"fixture-key secret upstream body"}`))
			return
		}
		value = gptregister.User{ID: f.user, Email: "owner@example.com", Points: 10000, Frozen: 99999}
	case r.URL.Path == "/open/v1/products":
		value = gptregister.Products{OrdersOpen: !f.closed, MaxQuantity: 200, Currency: "points", Products: []gptregister.Product{{UnitPrice: 7}, {NeedOAuth: true, UnitPrice: 15}}}
	case r.URL.Path == "/open/v1/registration-orders" && r.Method == "POST":
		if f.key == "" {
			f.key = r.Header.Get("Idempotency-Key")
			f.creates++
		} else if f.key != r.Header.Get("Idempotency-Key") {
			f.creates++
		}
		var input struct {
			Quantity int  `json:"quantity"`
			OAuth    bool `json:"need_oauth"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		if input.Quantity != f.quantity || input.OAuth != f.oauth || f.key == "" {
			w.WriteHeader(400)
			return
		}
		if f.loseCreate {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		value = order()
		w.WriteHeader(202)
	case r.URL.Path == "/open/v1/registration-orders/remote-order":
		value = order()
	case r.URL.Path == "/open/v1/accounts":
		if r.URL.Query().Get("order_id") != "remote-order" {
			w.WriteHeader(400)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		items := []gptregister.Account{}
		for i := f.delivered - offset; i > 0 && len(items) < 100; i-- {
			items = append(items, gptregister.Account{ID: fmt.Sprintf("a%d", i), OrderID: "remote-order", Email: fmt.Sprintf("delivered%d@example.com", i), NeedOAuth: f.oauth})
		}
		value = gptregister.AccountPage{Items: items, Total: f.delivered}
	case strings.HasSuffix(r.URL.Path, "/credentials"):
		if f.credentialStatus != 0 {
			w.WriteHeader(f.credentialStatus)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		id := strings.TrimPrefix(parts[len(parts)-2], "a")
		c := gptregister.Credentials{Email: "delivered" + id + "@example.com", Password: "credential-password", TOTP: "JBSWY3DPEHPK3PXP"}
		if f.oauth {
			c.OAuth = map[string]json.RawMessage{"access_token": json.RawMessage(`"access-fixture"`), "refresh_token": json.RawMessage(`"refresh-fixture"`)}
		}
		value = c
	default:
		w.WriteHeader(404)
		return
	}
	json.NewEncoder(w).Encode(value)
}
func newRegisterTest(t *testing.T, f *registerFake) (*Server, *httptest.Server, string) {
	t.Helper()
	remote := httptest.NewServer(f)
	t.Cleanup(remote.Close)
	dir := t.TempDir()
	db, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{store: db}
	t.Cleanup(func() { s.store.Close() })
	_, e = db.SaveGPTRegisterSettings(gptregister.Submission{Settings: gptregister.Settings{URL: remote.URL, EnterPro: true}, APIKey: "fixture-key"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.CreateGPTRegisterJob("fixture-browser-intent", f.quantity, f.oauth)
	if e != nil {
		t.Fatal(e)
	}
	return s, remote, dir
}
func syncRegisterTest(t *testing.T, s *Server, advance bool) (model.GPTRegisterJob, error) {
	t.Helper()
	now := time.Now()
	if advance {
		now = now.Add(2 * time.Minute)
	}
	j, cfg, lease, e := s.store.ClaimGPTRegisterJob(now)
	if e != nil {
		t.Fatal(e)
	}
	c, e := gptregister.New(cfg.URL, cfg.APIKey)
	if e != nil {
		t.Fatal(e)
	}
	e = s.syncGPTRegisterJob(context.Background(), c, &j, lease)
	if e == nil {
		j.NextAt = time.Now()
		if err := s.store.SaveGPTRegisterJob(j, lease, true); err != nil {
			t.Fatal(err)
		}
	}
	return j, e
}
func TestGPTRegisterProgressivePartialAndDestination(t *testing.T) {
	f := &registerFake{quantity: 3, oauth: true, status: "running", delivered: 1, user: "u1"}
	s, _, _ := newRegisterTest(t, f)
	j, e := syncRegisterTest(t, s, false)
	if e != nil || j.State != "syncing" || j.Imported != 1 {
		t.Fatal(j, e)
	}
	p, c, e := s.store.MailAccountCredential("delivered1@example.com")
	if e != nil || p.ManagementScope != "pro" || p.OAuthStatus != "completed" || c.RefreshToken != "refresh-fixture" || p.ProWorkflowRunning || p.ProAuto.ID != "" {
		t.Fatal("bad import", e)
	}
	f.mu.Lock()
	f.delivered = 2
	f.status = "partial"
	f.mu.Unlock()
	j, e = syncRegisterTest(t, s, false)
	if e != nil || j.State != "completed" || j.Imported != 2 || j.RemoteStatus != "partial" {
		t.Fatal(j, e)
	}
	if len(s.store.MailAccounts()) != 2 || f.creates != 1 {
		t.Fatal("duplicate or missing import")
	}
	raw, _ := json.Marshal(j)
	if strings.Contains(string(raw), "fixture-key") || strings.Contains(string(raw), "credential-password") {
		t.Fatal("job leaks secret")
	}
}
func TestGPTRegisterLostCreateResponseSurvivesRestart(t *testing.T) {
	f := &registerFake{quantity: 1, status: "completed", delivered: 1, user: "u1", loseCreate: true}
	s, _, dir := newRegisterTest(t, f)
	_, e := syncRegisterTest(t, s, false)
	if e == nil {
		t.Fatal("expected interrupted POST")
	}
	f.mu.Lock()
	f.loseCreate = false
	f.closed = true
	f.mu.Unlock()
	if e = s.store.Close(); e != nil {
		t.Fatal(e)
	}
	db, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	s.store = db
	j, e := syncRegisterTest(t, s, true)
	if e != nil || j.State != "completed" || j.Imported != 1 || f.creates != 1 {
		t.Fatal("lost response caused new paid order", j, e, f.creates)
	}
	// Replay browser submission after completion still returns the same task.
	old, e := s.store.CreateGPTRegisterJob("fixture-browser-intent", 1, false)
	if e != nil || old.ID != j.ID {
		t.Fatal("browser replay created another task", e)
	}
}

func TestGPTRegisterPagedDeliveryAndReview(t *testing.T) {
	f := &registerFake{quantity: 105, status: "review", delivered: 105, user: "u1"}
	s, _, _ := newRegisterTest(t, f)
	var j model.GPTRegisterJob
	for i := 0; i < 19; i++ {
		var err error
		j, err = syncRegisterTest(t, s, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	if j.Imported != 105 || j.State != "syncing" || f.creates != 1 {
		t.Fatal("paging or review replayed registration", j, f.creates)
	}
	f.mu.Lock()
	f.status = "completed"
	f.mu.Unlock()
	j, err := syncRegisterTest(t, s, false)
	if err != nil || j.State != "completed" || j.Imported != 105 {
		t.Fatal(j, err)
	}
}

func TestGPTRegisterEmptyTerminalOrders(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := &registerFake{quantity: 1, status: status, user: "u1"}
			s, _, _ := newRegisterTest(t, f)
			j, err := syncRegisterTest(t, s, false)
			if err != nil || j.State != "completed" || j.RemoteStatus != status || j.Imported != 0 {
				t.Fatal(j, err)
			}
		})
	}
}
func TestGPTRegisterPermissionsAndRateLimits(t *testing.T) {
	for _, code := range []int{401, 403, 429, 503} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			f := &registerFake{quantity: 1, status: "running", user: "u1", meStatus: code}
			s, _, _ := newRegisterTest(t, f)
			before := time.Now()
			s.runGPTRegisterOnce(context.Background())
			rows, e := s.store.GPTRegisterJobs()
			if e != nil || len(rows) != 1 {
				t.Fatal(e)
			}
			j := rows[0]
			expected := "retrying"
			if code == 401 || code == 403 {
				expected = "paused"
			}
			if j.State != expected || f.creates != 0 || strings.Contains(j.Message, "fixture-key") {
				t.Fatal(j, f.creates)
			}
			if code == 429 && j.NextAt.Before(before.Add(90*time.Second)) {
				t.Fatal("ignored Retry-After")
			}
		})
	}
}
func TestGPTRegisterKeyRotationRejectsDifferentOwner(t *testing.T) {
	f := &registerFake{quantity: 1, status: "running", user: "u1"}
	s, _, _ := newRegisterTest(t, f)
	if _, e := syncRegisterTest(t, s, false); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	f.user = "another-user"
	f.mu.Unlock()
	j, e := syncRegisterTest(t, s, false)
	if e == nil || !strings.Contains(e.Error(), "原任务不一致") || j.RemoteUserID != "u1" || f.creates != 1 {
		t.Fatal("changed user accepted", e)
	}
}
func TestGPTRegisterHTTPAuthReadOnlyTestAndSecretRedaction(t *testing.T) {
	f := &registerFake{quantity: 1, status: "running", user: "u1"}
	s, remote, _ := newRegisterTest(t, f)
	s.sessions = map[string]time.Time{}
	mux := http.NewServeMux()
	s.gptRegisterRoutes(mux)
	handler := s.authMiddleware(mux)
	cookie := &http.Cookie{Name: authCookieName, Value: s.createSession()}
	call := func(method, path, body, origin string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if auth {
			r.AddCookie(cookie)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/api/mail/gptregister/settings", "", "", false); w.Code != 401 {
		t.Fatal("public credentials settings")
	}
	if w := call("POST", "/api/mail/gptregister/jobs", `{"request_key":"evil-cross-site","quantity":1,"need_oauth":false}`, "https://evil.example", true); w.Code != 403 {
		t.Fatal("CSRF accepted", w.Code)
	}
	if w := call("GET", "/api/mail/gptregister/settings", "", "", true); w.Code != 200 || strings.Contains(w.Body.String(), "fixture-key") {
		t.Fatal("secret echoed", w.Code)
	}
	body, _ := json.Marshal(map[string]any{"url": remote.URL})
	if w := call("POST", "/api/mail/gptregister/test", string(body), "", true); w.Code != 200 || f.creates != 0 {
		t.Fatal("test created a paid order or failed", w.Code)
	}
	for i := 0; i < 2; i++ {
		if w := call("POST", "/api/mail/gptregister/jobs", `{"request_key":"fixture-browser-intent","quantity":1,"need_oauth":false}`, "", true); w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	rows, e := s.store.GPTRegisterJobs()
	if e != nil || len(rows) != 1 {
		t.Fatal("duplicate HTTP intents", len(rows), e)
	}
}
