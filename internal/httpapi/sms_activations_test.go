package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/herosms"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func heroFixture(t *testing.T, handler http.HandlerFunc) (*Server, *httptest.Server) {
	t.Helper()
	remote := httptest.NewServer(handler)
	t.Cleanup(remote.Close)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.SaveSMSConfig("hero_sms", map[string]any{"api_key": "fixture-key", "base_url": remote.URL, "service": "dr", "country": "52"}, true); err != nil {
		t.Fatal(err)
	}
	return &Server{store: db, auditQueue: make(chan model.AutoRotationEvent, 200)}, remote
}

func TestHeroRPCOwnershipReplaceAndExitCleanup(t *testing.T) {
	var calls []string
	s, _ := heroFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/activations":
			_, _ = w.Write([]byte(`{"data":[{"id":1,"phone":"66990000001"}]}`))
		case "POST /api/v1/activations/1/replace":
			_, _ = w.Write([]byte(`{"data":[{"id":2,"phone":"66990000002"}]}`))
		case "DELETE /api/v1/activations/2":
			w.WriteHeader(204)
		default:
			t.Error("unexpected request", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	p := &oauthProtocolRPC{server: s, ctx: ctx, email: "fixture@example.com"}
	if _, err := p.call("hero_acquire", nil); err != nil {
		t.Fatal(err)
	}
	other := &oauthProtocolRPC{server: s, ctx: ctx, email: "other@example.com"}
	if _, err := other.call("hero_release", map[string]any{"id": "1"}); err == nil {
		t.Fatal("cross-job release allowed")
	}
	if _, err := p.call("hero_replace", map[string]any{"id": "1"}); err != nil {
		t.Fatal(err)
	}
	cancel()
	p.close()
	due, err := s.store.DueSMSActivations(time.Now())
	if err != nil || len(due) != 1 || due[0].ID != "2" {
		t.Fatalf("cleanup %v %v", due, err)
	}
	s.cleanupHeroActivation(context.Background(), due[0])
	due, err = s.store.DueSMSActivations(time.Now().Add(time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatalf("still pending %v %v", due, err)
	}
	if len(calls) != 3 {
		t.Fatal(calls)
	}
}

func TestHeroCleanupDenialRetryAndNoFalseSuccess(t *testing.T) {
	var deny atomic.Bool
	deny.Store(true)
	s, remote := heroFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"title":"No OTP"}`))
			return
		}
		if deny.Load() {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"title":"EARLY_CANCEL_DENIED"}`))
			return
		}
		w.WriteHeader(204)
	})
	cfg := herosms.Config{BaseURL: remote.URL, APIKey: "fixture-key"}
	_ = s.store.SaveSMSActivation("5", "fixture@example.com", cfg)
	_ = s.store.QueueSMSActivation("5", false)
	items, _ := s.store.DueSMSActivations(time.Now())
	s.cleanupHeroActivation(context.Background(), items[0])
	items, _ = s.store.DueSMSActivations(time.Now().Add(time.Hour))
	if len(items) != 1 || items[0].Attempts != 1 {
		t.Fatal(items)
	}
	select {
	case e := <-s.auditQueue:
		if e.Details["event"] != "cancel_pending" || e.HTTPStatus != 422 {
			t.Fatal(e)
		}
	default:
		t.Fatal("missing retry event")
	}
	deny.Store(false)
	s.cleanupHeroActivation(context.Background(), items[0])
	items, _ = s.store.DueSMSActivations(time.Now().Add(time.Hour))
	if len(items) != 0 {
		t.Fatal(items)
	}
}

func TestHeroFinishWhenOTPAlreadyReceived(t *testing.T) {
	var finished bool
	s, remote := heroFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "DELETE":
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"title":"OTP already received"}`))
		case "GET":
			_, _ = w.Write([]byte(`{"data":{"smsCode":"123456"}}`))
		case "POST":
			finished = true
			w.WriteHeader(204)
		}
	})
	_ = s.store.SaveSMSActivation("7", "fixture@example.com", herosms.Config{BaseURL: remote.URL, APIKey: "fixture-key"})
	_ = s.store.QueueSMSActivation("7", false)
	items, _ := s.store.DueSMSActivations(time.Now())
	s.cleanupHeroActivation(context.Background(), items[0])
	if !finished {
		t.Fatal("received OTP activation left active")
	}
}

func TestHeroConcurrentJobsDoNotShareActivations(t *testing.T) {
	var count atomic.Int64
	s, _ := heroFixture(t, func(w http.ResponseWriter, r *http.Request) {
		id := count.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": id, "phone": fmt.Sprint(66990000000 + id)}}})
	})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := &oauthProtocolRPC{server: s, ctx: context.Background(), email: fmt.Sprintf("account%d@example.com", i)}
			defer p.close()
			if _, err := p.call("hero_acquire", nil); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	items, _ := s.store.DueSMSActivations(time.Now())
	if len(items) != 6 || count.Load() != 6 {
		t.Fatal(len(items), count.Load())
	}
	p := &oauthProtocolRPC{server: s, ctx: context.Background(), email: "account0@example.com"}
	defer p.close()
	if _, err := p.call("hero_acquire", nil); err != nil {
		t.Fatal("pending cleanup blocked new OAuth attempt", err)
	}
	if count.Load() != 7 {
		t.Fatal("unexpected purchase count", count.Load())
	}
}

func TestHeroCancelCooldownDoesNotBlockNextNumber(t *testing.T) {
	var purchased atomic.Int64
	var denyCancel atomic.Bool
	denyCancel.Store(true)
	s, _ := heroFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/activations":
			id := purchased.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": id, "phone": fmt.Sprint(66990000000 + id)}}})
		case "DELETE /api/v1/activations/1":
			if denyCancel.Load() {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"title":"EARLY_CANCEL_DENIED","details":"You can't cancel activation in first 2 minutes"}`))
				return
			}
			w.WriteHeader(204)
		case "GET /api/v1/activations/2/otp/last":
			_, _ = w.Write([]byte(`{"data":{"smsCode":"012345"}}`))
		case "POST /api/v1/activations/2/finish":
			w.WriteHeader(204)
		default:
			t.Error("unexpected request", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	})
	p := &oauthProtocolRPC{server: s, ctx: context.Background(), email: "fixture@example.com"}
	defer p.close()
	if _, err := p.call("hero_acquire", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.call("hero_release", map[string]any{"id": "1", "finish": false}); err != nil {
		t.Fatal(err)
	}
	items, err := s.store.DueSMSActivations(time.Now())
	if err != nil || len(items) != 1 {
		t.Fatalf("old number not queued: %v %v", items, err)
	}
	s.cleanupHeroActivation(context.Background(), items[0])
	if _, err := p.call("hero_acquire", nil); err != nil {
		t.Fatal("cooldown blocked next number", err)
	}
	other := &oauthProtocolRPC{server: s, ctx: context.Background(), email: p.email}
	if _, err := other.call("hero_acquire", nil); err == nil {
		t.Fatal("same email bought a second in-use number")
	}
	result, err := p.call("hero_fetch", map[string]any{"id": "2"})
	if err != nil || result.(map[string]any)["code"] != "012345" {
		t.Fatalf("new number OTP: %v %v", result, err)
	}
	if _, err := p.call("hero_release", map[string]any{"id": "2", "finish": true}); err != nil {
		t.Fatal(err)
	}
	items, err = s.store.DueSMSActivations(time.Now().Add(time.Hour))
	if err != nil || len(items) != 2 {
		t.Fatalf("both activations must stay tracked: %v %v", items, err)
	}
	for _, item := range items {
		if item.ID == "2" {
			s.cleanupHeroActivation(context.Background(), item)
		}
	}
	items, err = s.store.DueSMSActivations(time.Now().Add(time.Hour))
	if err != nil || len(items) != 1 || items[0].ID != "1" || items[0].Attempts != 1 {
		t.Fatalf("old number lost during new number completion: %v %v", items, err)
	}
	denyCancel.Store(false)
	s.cleanupHeroActivation(context.Background(), items[0])
	items, err = s.store.DueSMSActivations(time.Now().Add(time.Hour))
	if err != nil || len(items) != 0 || purchased.Load() != 2 {
		t.Fatalf("cleanup=%v purchases=%d err=%v", items, purchased.Load(), err)
	}
}

func TestHeroConcurrentSameEmailOnlyBuysOneInUseNumber(t *testing.T) {
	var purchased atomic.Int64
	s, _ := heroFixture(t, func(w http.ResponseWriter, r *http.Request) {
		id := purchased.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": id, "phone": fmt.Sprint(66990000000 + id)}}})
	})
	jobs := make(chan *oauthProtocolRPC, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := &oauthProtocolRPC{server: s, ctx: context.Background(), email: "fixture@example.com"}
			if _, err := p.call("hero_acquire", nil); err == nil {
				jobs <- p
			}
		}()
	}
	wg.Wait()
	close(jobs)
	succeeded := 0
	for p := range jobs {
		succeeded++
		p.close()
	}
	if succeeded != 1 || purchased.Load() != 1 {
		t.Fatalf("concurrent purchases=%d successes=%d", purchased.Load(), succeeded)
	}
}

func TestHero404MustCheckAllActivePages(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			s, remote := heroFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					w.WriteHeader(404)
					_, _ = w.Write([]byte(`{"title":"Not found"}`))
					return
				}
				rows := []map[string]any{}
				if r.URL.Query().Get("page") == "1" {
					for i := 1; i <= 25; i++ {
						rows = append(rows, map[string]any{"id": i})
					}
				}
				if r.URL.Query().Get("page") == "2" && present {
					rows = append(rows, map[string]any{"id": 1234567890123})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
			})
			_ = s.store.SaveSMSActivation("1234567890123", "fixture@example.com", herosms.Config{BaseURL: remote.URL, APIKey: "fixture-key"})
			_ = s.store.QueueSMSActivation("1234567890123", false)
			items, _ := s.store.DueSMSActivations(time.Now())
			s.cleanupHeroActivation(context.Background(), items[0])
			items, _ = s.store.DueSMSActivations(time.Now().Add(time.Hour))
			if (len(items) == 1) != present {
				t.Fatalf("active=%v pending=%v", present, items)
			}
		})
	}
}

func TestHeroRetryLimitStopsAndLogsFailure(t *testing.T) {
	s, remote := heroFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"title":"temporarily unavailable"}`))
	})
	_ = s.store.SaveSMSActivation("9", "fixture@example.com", herosms.Config{BaseURL: remote.URL, APIKey: "fixture-key"})
	_ = s.store.UpdateSMSActivation("9", "cancel_pending", "", 9, time.Now().Add(-time.Hour))
	items, _ := s.store.DueSMSActivations(time.Now())
	s.cleanupHeroActivation(context.Background(), items[0])
	items, _ = s.store.DueSMSActivations(time.Now().Add(time.Hour))
	if len(items) != 0 {
		t.Fatal("unbounded retry")
	}
	e := <-s.auditQueue
	if e.Details["event"] != "cleanup_failed" || e.HTTPStatus != 500 {
		t.Fatal(e)
	}
}

func TestHeroFailedReplaceKeepsOldActivationForCleanup(t *testing.T) {
	calls := 0
	s, _ := heroFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/api/v1/activations" {
			_, _ = w.Write([]byte(`{"data":[{"id":1,"phone":"66990000001"}]}`))
			return
		}
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"title":"not replaceable yet"}`))
	})
	p := &oauthProtocolRPC{server: s, ctx: context.Background(), email: "fixture@example.com"}
	if _, err := p.call("hero_acquire", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.call("hero_replace", map[string]any{"id": "1"}); err == nil {
		t.Fatal("replace should fail")
	}
	p.close()
	items, _ := s.store.DueSMSActivations(time.Now())
	if len(items) != 1 || items[0].ID != "1" || calls != 2 {
		t.Fatal(items, calls)
	}
}
