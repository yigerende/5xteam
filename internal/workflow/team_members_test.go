package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestFindMemberByEmailUsesSeatQueryTransportAndExactMatch(t *testing.T) {
	for _, tc := range []struct {
		name, body          string
		status              int
		found, active, fail bool
	}{
		{"har", `{"items":[{"id":"user-child","email":"Child+test@example.com","seat_type":"prolite","deactivated_time":null}],"total":1}`, 200, true, true, false},
		{"fuzzy_only", `{"items":[{"id":"other","email":"not-child+test@example.com","seat_type":"prolite"}]}`, 200, false, false, false},
		{"empty", `{"items":[],"total":0}`, 200, false, false, false},
		{"deactivated", `{"items":[{"id":"child","email":"child+test@example.com","seat_type":"prolite","deactivated_time":"2026-09-27T09:00:00Z"}]}`, 200, true, false, false},
		{"missing_id", `{"items":[{"email":"child+test@example.com","seat_type":"prolite"}]}`, 200, false, false, true},
		{"malformed", `{"unexpected":true}`, 200, false, false, true},
		{"denied", `{"error":"denied"}`, 403, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/accounts/team-1/users" || r.URL.Query().Get("query") != "child+test@example.com" || r.URL.Query().Get("limit") != "25" || r.URL.Query().Get("offset") != "0" {
					t.Errorf("unexpected member request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer mother-token" || r.Header.Get("chatgpt-account-id") != "team-1" || !strings.Contains(r.Header.Get("User-Agent"), "Chrome/136") || !strings.Contains(r.Header.Get("Referer"), "/admin/members") {
					t.Error("member query did not use seat-query headers")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer upstream.Close()
			settings := model.DefaultSettings()
			settings.BaseURL, settings.ProxyURL = upstream.URL, upstream.URL
			client, err := NewClient(settings)
			if err != nil {
				t.Fatal(err)
			}
			member, err := client.FindMemberByEmail(context.Background(), "mother-token", "team-1", "child+test@example.com")
			if (err != nil) != tc.fail || (member != nil) != tc.found {
				t.Fatalf("member=%+v err=%v", member, err)
			}
			if member != nil && (member.Active != tc.active || member.SeatType != "prolite") {
				t.Fatalf("member=%+v", member)
			}
		})
	}
}

func TestFindMemberByEmailPaginatesFilteredResults(t *testing.T) {
	var offsets []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		if r.URL.Query().Get("query") != "child@example.com" {
			t.Error("unfiltered member query")
		}
		if offset == "0" {
			items := make([]map[string]string, 25)
			for i := range items {
				items[i] = map[string]string{"id": "other", "email": "other@example.com"}
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items})
			return
		}
		fmt.Fprint(w, `{"items":[{"id":"child","email":"child@example.com","seat_type":"prolite"}]}`)
	}))
	defer upstream.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = upstream.URL
	client, err := NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	member, err := client.FindMemberByEmail(context.Background(), "mother", "team-1", "child@example.com")
	if err != nil || member == nil || strings.Join(offsets, ",") != "0,25" {
		t.Fatalf("member=%+v err=%v offsets=%v", member, err, offsets)
	}
}
