package sub2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

const qualityTestStart = "data: {\"type\":\"test_start\",\"model\":\"gpt-6-astra\",\"data\":{\"custom_prompt\":true,\"reasoning_effort\":\"xhigh\"}}\n\n"
const qualityTestContent = "data: {\"type\":\"content\",\"text\":\"FINAL_ANSWER=29\"}\n\n"
const qualityTestComplete = "data: {\"type\":\"test_complete\",\"success\":true}\n\n"

func TestQualityTestStream(t *testing.T) {
	for _, tc := range []struct {
		name, stream string
		complete     bool
		status       int
		wantErr      bool
	}{
		{"success", qualityTestStart + qualityTestContent + qualityTestComplete, true, 200, false},
		{"crlf", strings.ReplaceAll(qualityTestStart+qualityTestContent+qualityTestComplete, "\n", "\r\n"), true, 200, false},
		{"no_final_newline", strings.TrimSpace(qualityTestStart + qualityTestContent + qualityTestComplete), true, 200, false},
		{"truncated", qualityTestStart + qualityTestContent, false, 0, true},
		{"done_only", qualityTestStart + qualityTestContent + "data: [DONE]\n\n", false, 0, true},
		{"empty", qualityTestStart + qualityTestComplete, false, 0, true},
		{"old_sub2", "data: {\"type\":\"test_start\"}\n\n" + qualityTestContent + qualityTestComplete, false, 0, true},
		{"wrong_effort", strings.ReplaceAll(qualityTestStart, "xhigh", "low") + qualityTestContent + qualityTestComplete, false, 0, true},
		{"no_start", qualityTestContent + qualityTestComplete, false, 0, true},
		{"repeated_start", qualityTestStart + qualityTestStart + qualityTestComplete, false, 0, true},
		{"invalid_json", qualityTestStart + "data: {broken}\n\n", false, 0, true},
		{"failed_complete", qualityTestStart + "data: {\"type\":\"test_complete\"}\n\n", false, 0, true},
		{"upstream401", qualityTestStart + "data: {\"type\":\"error\",\"error\":\"API returned 401: unauthorized\"}\n\n", false, 401, false},
		{"upstream429", qualityTestStart + "data: {\"type\":\"error\",\"error\":\"API returned 429: limited\"}\n\n", false, 429, false},
		{"incidental401", qualityTestStart + "data: {\"type\":\"error\",\"error\":\"network request id 401 failed\"}\n\n", false, 0, false},
		{"too_large", "data: " + strings.Repeat("x", 2<<20), false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := readQualityTestStream(strings.NewReader(tc.stream), QualityProbeResult{AccountID: 71}, "xhigh")
			if (err != nil) != tc.wantErr || out.Complete != tc.complete || out.HTTPStatus != tc.status {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if out.Complete && (out.Text != "FINAL_ANSWER=29" || out.AccountID != 71 || out.Model != "gpt-6-astra") {
				t.Fatalf("out=%+v", out)
			}
		})
	}
}

func TestQualityExistingAccountEndpointAndAdminRetry(t *testing.T) {
	var calls, logins atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			logins.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"admin-token"}}`)
			return
		}
		if r.URL.Path != "/api/v1/admin/accounts/71/test" || r.Method != "POST" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 3 || body["model_id"] != "gpt-6-astra" || body["reasoning_effort"] != "xhigh" || !strings.Contains(fmt.Sprint(body["prompt"]), "12+17") || strings.Contains(fmt.Sprint(body["prompt"]), "=29") {
			t.Errorf("body=%+v", body)
		}
		if r.Header.Get("X-Quality-Global-Proxy") != "" || r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("Authorization") != "Bearer admin-token" {
			t.Error("unexpected headers")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, qualityTestStart)
		w.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond)
		fmt.Fprint(w, qualityTestContent+qualityTestComplete)
	}))
	defer srv.Close()
	q := model.DefaultQualitySettings()
	q.Prompt = "12+17"
	q.Answer = "29"
	c := New()
	c.http.Timeout = 10 * time.Millisecond
	out, err := c.ProbeQuality(context.Background(), model.Sub2Settings{URL: srv.URL, Email: "admin@example.com"}, "password", 71, q)
	if err != nil || !out.Complete || out.DurationMS < 20 || calls.Load() != 2 || logins.Load() != 2 {
		t.Fatalf("out=%+v err=%v calls=%d logins=%d", out, err, calls.Load(), logins.Load())
	}
}

func TestQualityTestTimeoutAndHTTPFailure(t *testing.T) {
	for _, code := range []int{0, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/login" {
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"token"}}`)
					return
				}
				if code != 0 {
					w.WriteHeader(code)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, qualityTestStart)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			out, err := New().ProbeQuality(ctx, model.Sub2Settings{URL: srv.URL, Email: "admin@example.com"}, "pw", 71, model.DefaultQualitySettings())
			if err == nil || out.Complete || out.HTTPStatus == 401 {
				t.Fatalf("admin failures are not upstream401: %+v %v", out, err)
			}
		})
	}
}
