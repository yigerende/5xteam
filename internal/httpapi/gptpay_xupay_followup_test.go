package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func xupayTestInput(id string) map[string]any {
	return map[string]any{"request_id": id, "provider": gptpay.XUPay, "plan_code": "pro20", "country": "PH", "card_id": "test-card"}
}

func xupayFixtureSession(t *testing.T, s *Server, email string) {
	t.Helper()
	cfg, _, _ := s.store.GPTPaySettings()
	cfg.Provider, cfg.Country, cfg.TermsAccepted = gptpay.XUPay, "PH", true
	if _, err := s.store.SaveGPTPaySettings(cfg, "xupay-key"); err != nil {
		t.Fatal(err)
	}
	p, c, err := s.store.MailAccountCredential(email)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"accessToken": c.AccessToken, "session_token": "fixture-web-session", "user": map[string]any{"email": email, "name": "fixture"}, "account": map[string]any{"id": "personal-id", "planType": "free"}, "expires": "2099-01-01T00:00:00Z", "cookies": "__Secure-next-auth.session-token=fixture-cookie"})
	c.ChatGPTSession = string(raw)
	if _, err = s.store.SaveMailAccount(p, c); err != nil {
		t.Fatal(err)
	}
}

func TestXUPayRateLimitPersistsOriginalRetryBudget(t *testing.T) {
	calls := 0
	var original []byte
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/api/v1/orders" || r.Header.Get("Idempotency-Key") != "pro-rate-budget-original" {
			t.Error("retry changed original route/id")
		}
		if calls == 1 {
			original = raw
		} else if !bytes.Equal(original, raw) {
			t.Error("retry mutated session/card/body")
		}
		var body struct {
			Session map[string]any `json:"session"`
			Keep    bool           `json:"keep_renewal"`
		}
		_ = json.Unmarshal(raw, &body)
		if body.Session["sessionToken"] != "fixture-web-session" || body.Session["expires"] != "2099-01-01T00:00:00Z" || body.Session["cookies"] == nil || body.Keep {
			t.Error("full session or default cancellation lost")
		}
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"rate limited"}`)
	})
	xupayFixtureSession(t, s, email)
	o := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, xupayTestInput("rate-budget-original")))
	if !o.CanRetryRateLimit() || o.SubmissionUncertain || calls != 1 {
		t.Fatal(o, calls)
	}
	// No immediate replay even when the user clicks. The persisted due time wins.
	_ = payOrder(t, payCall(t, s.retryGPTPayOrder, "id", o.ID, map[string]any{}))
	if calls != 1 {
		t.Fatal("ignored Retry-After")
	}
	if due, err := st.GPTPayOrdersDue(time.Now(), 20); err != nil || len(due) != 0 {
		t.Fatal("scheduled too early", due, err)
	}
	s.Close()
	recovered, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	for i := 1; i <= 3; i++ {
		time.Sleep(max(time.Millisecond, time.Until(*o.NextStatusCheckAt)+10*time.Millisecond))
		recovered.pollGPTPayOrders(context.Background(), time.Now())
		o, _, err = st.GPTPayOrder(o.ID)
		if err != nil || o.RateLimitRetries != i || calls != i+1 {
			t.Fatal("restart retry failed", o, calls, err)
		}
	}
	if o.CanRetryRateLimit() || o.NextStatusCheckAt != nil || o.Status != "submission_unknown" || o.SubmissionUncertain {
		t.Fatal("retry bound or uncertainty incorrect", o)
	}
	recovered.pollGPTPayOrders(context.Background(), time.Now().Add(time.Hour))
	if calls != 4 {
		t.Fatal("exceeded three retries")
	}
	_, err = st.UpdateProAccount(email, func(p *model.MailAccountProfile) {
		p.ProAuto = model.ProAutoState{ID: "flow", Stage: "recharge", Status: "failed", OrderID: o.ID, Steps: map[string]string{"recharge": "failed"}, Replacement: &model.ProRechargeReplacement{RootEmail: email, Limit: 2}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.QueueProRechargeReplacement(email, "flow"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := st.MailAccountCredential(email)
	if p.ProAuto.Replacement.Attempts != 0 || p.ProAuto.Replacement.Pending {
		t.Fatal("consumed replacement budget")
	}
}

func TestXUPayOriginalCheckAcceptanceAndUncertainRecovery(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			checks, creates := 0, 0
			active := false
			allowed := true
			s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET":
					activation, renewal := "review", "review"
					if active {
						activation = "active"
					}
					fmt.Fprintf(w, `{"order":{"id":"existing","product_id":"pro","workflow":"subscription","status":"succeeded","activation":%q,"renewal":%q},"payment":{"state":"succeeded","can_check":%t,"fee_state":"charged"}}`, activation, renewal, allowed)
				case r.URL.Path == "/api/v1/orders/existing/payment/check":
					checks++
					if uncertain {
						w.WriteHeader(503)
						fmt.Fprint(w, `{"error":"unknown acceptance"}`)
					} else {
						fmt.Fprint(w, `{"accepted":true}`)
					}
				default:
					creates++
					fmt.Fprint(w, `{"id":"existing","status":"processing"}`)
				}
			})
			xupayFixtureSession(t, s, email)
			o := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, xupayTestInput("check-original-order")))
			o = payOrder(t, payCall(t, s.refreshGPTPayOrder, "id", o.ID, map[string]any{}))
			if o.Status != "manual_review" {
				t.Fatal(o)
			}
			allowed = false
			if rec := payCall(t, s.checkXUPayOrder, "id", o.ID, map[string]any{}); rec.Code != 409 || checks != 0 {
				t.Fatal("ignored fresh can_check", rec.Code, checks)
			}
			allowed = true
			rec := payCall(t, s.checkXUPayOrder, "id", o.ID, map[string]any{})
			want := 202
			if uncertain {
				want = 502
			}
			if rec.Code != want || checks != 1 || creates != 1 {
				t.Fatal(rec.Code, rec.Body.String(), checks, creates)
			}
			o, _, _ = st.GPTPayOrder(o.ID)
			if o.Status == "success" || !o.NeedsStatusPoll() || o.CheckPending != uncertain {
				t.Fatal("accepted falsely means success or stopped polling", o)
			}
			if rec = payCall(t, s.checkXUPayOrder, "id", o.ID, map[string]any{}); rec.Code != 409 || checks != 1 {
				t.Fatal("duplicate original check")
			}
			active = true
			allowed = false
			past := time.Now().UTC().Add(-time.Second)
			o.NextStatusCheckAt = &past
			if err := st.UpdateGPTPayOrder(o); err != nil {
				t.Fatal(err)
			}
			s.pollGPTPayOrders(context.Background(), time.Now())
			o, _, _ = st.GPTPayOrder(o.ID)
			p, _, _ := st.MailAccountCredential(email)
			if o.Status != "success" || o.Remote.CancellationStatus != "failed" || o.CheckPending || o.NeedsStatusPoll() || p.ProManualStages["recharge"].Status != "completed" {
				t.Fatal("renewal failure blocked subscription success", o, p.ProManualStages)
			}
			if creates != 1 || checks != 1 {
				t.Fatal("recovery repeated payment/check", creates, checks)
			}
		})
	}
}

func TestXUPayReadRateLimitAndCheckAuthentication(t *testing.T) {
	reads := 0
	s, st, email := gptPayFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			fmt.Fprint(w, `{"id":"existing","status":"processing"}`)
			return
		}
		reads++
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"limited"}`)
	})
	xupayFixtureSession(t, s, email)
	o := payOrder(t, payCall(t, s.createGPTPayOrder, "email", email, xupayTestInput("read-rate-limit-order")))
	o, snap, _ := st.GPTPayOrder(o.ID)
	o, err := s.syncGPTPayOrderStatus(context.Background(), o, snap)
	if err == nil || o.NextStatusCheckAt == nil || time.Until(*o.NextStatusCheckAt) < 119*time.Second || o.StatusCheckError == "" {
		t.Fatal("query retry not persisted", o, err)
	}
	_, _ = s.syncGPTPayOrderStatus(context.Background(), o, snap)
	if reads != 1 {
		t.Fatal("read cooldown bypassed")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/gptpay/orders/"+o.ID+"/check", bytes.NewBufferString(`{}`)))
	if rec.Code != 401 {
		t.Fatal("new action missing authentication", rec.Code)
	}
}
