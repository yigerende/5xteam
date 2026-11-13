package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func TestSIWCMarkerSyncRetryUsesPinnedTargetAndNeverRerunsWorkflow(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	s := &Server{store: st}
	calls := 0
	status := http.StatusServiceUnavailable
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"access_token": "marker-admin"}})
		case "/api/v1/admin/accounts/7/space-workflow":
			calls++
			if r.Method != "PUT" || r.Header.Get("Authorization") != "Bearer marker-admin" {
				t.Error("invalid marker request")
			}
			var m model.SIWCWorkflowMarker
			if e := json.NewDecoder(r.Body).Decode(&m); e != nil {
				t.Error(e)
			}
			if !m.Completed || m.Stage != "merchant" || m.Email != "marker@example.com" {
				t.Errorf("wrong marker: %+v", m)
			}
			if status != 200 {
				w.WriteHeader(status)
				w.Write([]byte(`{"message":"private upstream diagnostic"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"revision": m.Revision, "applied": true}})
		default:
			t.Error("business endpoint called:", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	cfg := model.DefaultProSettings()
	cfg.Provider = "sub2"
	cfg.Sub2.URL = remote.URL
	cfg.Sub2.Email = "admin@example.com"
	cfg.Sub2.AuthMode = "siwc"
	if _, e = st.SaveProSettings(cfg, "test-password", ""); e != nil {
		t.Fatal(e)
	}
	target, e := st.SaveDownstreamTarget(model.DownstreamTarget{Scope: "pro", Provider: "sub2", Sub2: cfg.Sub2, Secret: "test-password"})
	if e != nil {
		t.Fatal(e)
	}
	email := "marker@example.com"
	if _, e = st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{}); e != nil {
		t.Fatal(e)
	}
	if _, e = st.UpdateMailAccountManagementScope(email, "pro"); e != nil {
		t.Fatal(e)
	}
	ref := model.ProDownstream(cfg)
	now := time.Now()
	if _, e = st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.Sub2AccountID = 7
		p.PushProvider = "sub2"
		p.ProRestockTargetID = target
		p.ProPushDownstream = &ref
		p.ProAuto = model.ProAutoState{ID: "flow", OrderID: "paid-order", Stage: "merchant", Status: "completed", Steps: map[string]string{"merchant": "completed"}}
		p.ProMerchant = &model.ProMerchant{RequestID: "request", CompletedAt: &now}
	}); e != nil {
		t.Fatal(e)
	}
	// A subsequent global setting change must not address account 7 on a new site.
	cfg.Sub2.URL = "http://127.0.0.1:1"
	if _, e = st.SaveProSettings(cfg, "test-password", ""); e != nil {
		t.Fatal(e)
	}
	client := sub2.New()
	run := func() {
		t.Helper()
		tasks, err := st.ClaimSIWCMarkers(time.Now().Add(time.Hour))
		if err != nil || len(tasks) != 1 {
			t.Fatalf("claim: %d %v", len(tasks), err)
		}
		s.syncSIWCMarker(context.Background(), client, tasks[0])
	}
	run()
	states, e := st.SIWCMarkerStates([]string{email})
	if e != nil || states[email].State != "retry" || strings.Contains(states[email].Error, "private") {
		t.Fatal(states, e)
	}
	p, _, e := st.MailAccountCredential(email)
	if e != nil || p.ProAuto.Status != "completed" || p.ProAuto.OrderID != "paid-order" {
		t.Fatal("marker error changed business state", e)
	}
	status = 200
	run()
	states, e = st.SIWCMarkerStates([]string{email})
	if e != nil || states[email].State != "synced" || calls != 2 {
		t.Fatal(states, calls, e)
	}
	// A caller retry after acknowledgement cannot resubmit any business request.
	if e = st.RetrySIWCMarker(email); e != nil {
		t.Fatal(e)
	}
	tasks, e := st.ClaimSIWCMarkers(time.Now().Add(time.Hour))
	if e != nil || len(tasks) != 0 {
		t.Fatal("already synced marker replayed", e)
	}
}
