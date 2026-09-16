package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func newMailInfoTestServer(t *testing.T, count int) *Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := st.Settings()
	settings.ProxyURL = "http://fixture-global-proxy.invalid:8080"
	if err = st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		email := fmt.Sprintf("info-%02d@example.com", i)
		if _, err = st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, AccessToken: "fixture-at-" + email, RefreshToken: "secret-rt"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st, auditStop: make(chan struct{})}
	t.Cleanup(func() { close(s.auditStop); s.stopMailInfoJobs(); st.Close() })
	return s
}
func startMailInfoTest(t *testing.T, s *Server, emails []string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"emails": emails})
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/api/mail/accounts/refresh-info", strings.NewReader(string(raw))).WithContext(ctx)
	w := httptest.NewRecorder()
	s.startMailGPTInfo(w, r)
	cancel()
	if w.Code != 202 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var payload struct {
		Data struct {
			JobID string `json:"job_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Data.JobID
}

type mailInfoTestResponse struct {
	Data struct {
		Job struct {
			Total, Done, OK, Fail, Partial int
			Status                         string
			Results                        []mailInfoTask
		} `json:"job"`
	} `json:"data"`
}

func waitMailInfoTest(t *testing.T, s *Server, id string) mailInfoTestResponse {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for {
		w := httptest.NewRecorder()
		s.mailGPTInfoStatus(w, httptest.NewRequest("GET", "/api/mail/accounts/refresh-info/status?job_id="+id+"&page=1&page_size=10", nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "fixture-at") || strings.Contains(w.Body.String(), "secret-rt") {
			t.Fatal("credentials leaked")
		}
		var p mailInfoTestResponse
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if p.Data.Job.Status == "done" {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatal("job timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func successfulMailInfo() model.MailGPTInfoResult {
	created := time.Unix(1700000000, 0).UTC()
	return model.MailGPTInfoResult{Check: model.GPTInfoCheck{Status: "success", CheckedAt: time.Now().UTC(), PlanSource: "entitlement", Plan: model.GPTInfoRequestStatus{OK: true, HTTPStatus: 200}, Me: model.GPTInfoRequestStatus{OK: true, HTTPStatus: 200}}, Plan: model.AccountPlanCheckResult{OK: true, CurrentPlanType: "free"}, CreatedAtOpenAI: &created}
}
func TestMailGPTInfoGlobalConcurrencyDedupAndIsolation(t *testing.T) {
	s := newMailInfoTestServer(t, 24)
	var active, maximum, calls atomic.Int32
	var startsMu sync.Mutex
	var starts []time.Time
	release := make(chan struct{})
	s.mailInfoRefresh = func(ctx context.Context, settings model.Settings, token string) model.MailGPTInfoResult {
		startsMu.Lock()
		starts = append(starts, time.Now())
		startsMu.Unlock()
		if settings.ProxyURL != "http://fixture-global-proxy.invalid:8080" {
			t.Error("wrong proxy")
		}
		calls.Add(1)
		n := active.Add(1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		defer active.Add(-1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return successfulMailInfo()
	}
	first, second := []string{}, []string{}
	for i := 0; i < 12; i++ {
		first = append(first, fmt.Sprintf("info-%02d@example.com", i))
	}
	for i := 12; i < 24; i++ {
		second = append(second, fmt.Sprintf("info-%02d@example.com", i))
	}
	id1 := startMailInfoTest(t, s, append(first, strings.ToUpper(first[0])))
	id2 := startMailInfoTest(t, s, second)
	id3 := startMailInfoTest(t, s, []string{first[0]})
	deadline := time.Now().Add(4 * time.Second)
	for active.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active.Load() != 4 {
		t.Errorf("active=%d want 4", active.Load())
	}
	other := newMailInfoTestServer(t, 0)
	w := httptest.NewRecorder()
	other.mailGPTInfoStatus(w, httptest.NewRequest("GET", "/api/mail/accounts/refresh-info/status?job_id="+id1, nil))
	if w.Code != 404 {
		t.Error("job accessible in another server/store")
	}
	close(release)
	p := waitMailInfoTest(t, s, id1)
	if p.Data.Job.Total != 12 || p.Data.Job.OK != 12 || len(p.Data.Job.Results) != 10 {
		t.Fatalf("pagination/counters: %+v", p)
	}
	waitMailInfoTest(t, s, id2)
	waitMailInfoTest(t, s, id3)
	if maximum.Load() > 4 || calls.Load() != 24 {
		t.Fatalf("peak=%d calls=%d", maximum.Load(), calls.Load())
	}
	startsMu.Lock()
	defer startsMu.Unlock()
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 380*time.Millisecond {
			t.Errorf("batch launches too close: %v", gap)
		}
	}
	profile, creds, err := s.store.MailAccountCredential(first[0])
	if err != nil || profile.CurrentPlanType != "free" || profile.CreatedAtOpenAI == nil || creds.RefreshToken != "secret-rt" {
		t.Fatal(profile, err)
	}
}

func TestMailGPTInfoUsesProPacingAndCancelsBeforeQuery(t *testing.T) {
	s := newMailInfoTestServer(t, 1)
	// A recent Pro request must also delay a mail refresh using the same gate.
	s.planCheckNext = time.Now().Add(150 * time.Millisecond)
	for i := 0; i < 3; i++ {
		expected := s.planCheckNext
		if err := s.waitForProPlanCheckSlot(context.Background()); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if now.Before(expected) {
			t.Fatal("query started before reserved slot")
		}
		if gap := s.planCheckNext.Sub(expected); gap < 400*time.Millisecond || gap > 700*time.Millisecond {
			t.Fatalf("configured launch interval=%v", gap)
		}
	}
	var calls atomic.Int32
	s.mailInfoRefresh = func(context.Context, model.Settings, string) model.MailGPTInfoResult {
		calls.Add(1)
		return successfulMailInfo()
	}
	profile, _, err := s.store.MailAccountCredential("info-00@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	task := &mailInfoTask{Email: profile.Email, accountID: profile.ID}
	s.refreshMailInfoTask(ctx, task, s.store.Settings())
	if calls.Load() != 0 || task.Status != "failed" {
		t.Fatalf("cancelled task executed: calls=%d status=%s", calls.Load(), task.Status)
	}
}
func TestMailGPTInfoSelectionMissingATPartialAndNoAutoRequests(t *testing.T) {
	s := newMailInfoTestServer(t, 3)
	_, err := s.store.SaveMailAccount(model.MailAccountProfile{Email: "empty@example.com"}, model.MailAccountCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s.mailInfoRefresh = func(context.Context, model.Settings, string) model.MailGPTInfoResult {
		calls.Add(1)
		r := successfulMailInfo()
		r.Check.Status = "partial"
		r.Check.Me = model.GPTInfoRequestStatus{HTTPStatus: 403, Error: "denied"}
		r.CreatedAtOpenAI = nil
		return r
	}
	w := httptest.NewRecorder()
	s.listMailAccounts(w, httptest.NewRequest("GET", "/api/mail/accounts?page=1&page_size=10", nil))
	if calls.Load() != 0 {
		t.Fatal("listing triggered refresh")
	}
	id := startMailInfoTest(t, s, []string{"info-01@example.com", "empty@example.com", "missing@example.com"})
	result := waitMailInfoTest(t, s, id).Data.Job
	if calls.Load() != 1 || result.Done != 3 || result.Partial != 1 || result.Fail != 2 {
		t.Fatalf("counts: %+v calls=%d", result, calls.Load())
	}
	for _, email := range []string{"info-00@example.com", "info-02@example.com"} {
		p, _, _ := s.store.MailAccountCredential(email)
		if p.GPTInfoCheck != nil {
			t.Fatal("unselected account updated")
		}
	}
}
func TestMailGPTInfoValidationSingleAndDeletedDuringRequest(t *testing.T) {
	s := newMailInfoTestServer(t, 1)
	for _, body := range []string{`{}`, `{"emails":["bad"]}`} {
		w := httptest.NewRecorder()
		s.startMailGPTInfo(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	started, release := make(chan struct{}), make(chan struct{})
	s.mailInfoRefresh = func(context.Context, model.Settings, string) model.MailGPTInfoResult {
		close(started)
		<-release
		return successfulMailInfo()
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.SetPathValue("email", "info-00@example.com")
	w := httptest.NewRecorder()
	s.startMailGPTInfo(w, r)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var payload struct {
		Data struct {
			ID string `json:"job_id"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &payload)
	<-started
	if err := s.store.DeleteMailAccount("info-00@example.com"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if job := waitMailInfoTest(t, s, payload.Data.ID).Data.Job; job.Fail != 1 {
		t.Fatal(job)
	}
	if _, _, err := s.store.MailAccountCredential("info-00@example.com"); err == nil {
		t.Fatal("deleted account recreated")
	}
	settings := s.store.Settings()
	settings.ProxyURL = ""
	s.store.SaveSettings(settings)
	w = httptest.NewRecorder()
	s.startMailGPTInfo(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"emails":["missing@example.com"]}`)))
	if w.Code != 400 {
		t.Fatal("missing proxy accepted")
	}
}
func TestMailGPTInfoShutdownAndExpiredTask(t *testing.T) {
	s := newMailInfoTestServer(t, 1)
	s.mailInfoRefresh = func(ctx context.Context, _ model.Settings, _ string) model.MailGPTInfoResult {
		<-ctx.Done()
		return successfulMailInfo()
	}
	id := startMailInfoTest(t, s, []string{"info-00@example.com"})
	// Use a separate channel to signal shutdown; cleanup still owns auditStop.
	s.mailInfoMu.Lock()
	s.mailInfoJobs["expired"] = &mailInfoJob{ID: "expired", CompletedAt: time.Now().Add(-time.Hour)}
	s.mailInfoMu.Unlock()
	w := httptest.NewRecorder()
	s.mailGPTInfoStatus(w, httptest.NewRequest("GET", "/api/mail/accounts/refresh-info/status?job_id=expired", nil))
	if w.Code != 404 {
		t.Fatal("expired job not pruned")
	}
	if id == "" {
		t.Fatal("missing job ID")
	}
}
