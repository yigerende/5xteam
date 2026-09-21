package gptpay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGPTPayCardParsing(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	for _, date := range []string{"202812", "2028-12", "2028/12", "12/28", "12/2028", "2812"} {
		v, err := ParseCard("4242 4242 4242 4242----"+date+"----012", now)
		if err != nil || v.Month != 12 || v.Year != 2028 || v.CVV != "012" || v.Number != "4242424242424242" {
			t.Fatalf("date %s: %#v %v", date, v, err)
		}
	}
	for _, line := range []string{"4242424242424242----202613----123", "4242424242424242----202608----123", "123----202812----123", "4242424242424242----202812----xx1", "4242424242424242----unknown----123", "bad"} {
		if _, err := ParseCard(line, now); err == nil {
			t.Fatalf("accepted %q", line)
		}
	}
}

func TestGPTPayResponseClassification(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		uncertain bool
	}{
		{402, `{"code":40201,"message":"do not expose secret"}`, false},
		{429, `{"code":42901}`, false},
		{200, `{"code":40101}`, false},
		{409, `{"code":40902}`, true},
		{503, `{"code":50301}`, true},
		{200, `{"data":{}}`, true},
		{201, `{"code":0,"data":{}}`, true},
		{200, `<html>secret</html>`, true},
	} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			_, _, err := New().Create(context.Background(), Snapshot{URL: srv.URL, APIKey: "key"}, "test-idempotency-key")
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Uncertain != tc.uncertain {
				t.Fatalf("error: %v", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("upstream raw content leaked")
			}
		})
	}
}
func TestGPTPayDoesNotFollowRedirectWithCredentials(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	_, _, err := New().Create(context.Background(), Snapshot{URL: source.URL, APIKey: "secret"}, "test-idempotency-key")
	if err == nil || called {
		t.Fatal("redirect forwarded payment credentials")
	}
}
