package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestAdminMembersLiveQueryUsesDedicatedProxy(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var globalCalls, memberCalls atomic.Int32
	global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalCalls.Add(1)
		http.Error(w, "global proxy must not be used", 500)
	}))
	defer global.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		memberCalls.Add(1)
		if r.Method != "GET" || r.URL.Host != "127.0.0.1:1" || r.URL.Path != "/backend-api/accounts/team-1/users" || r.Header.Get("Authorization") != "Bearer mother-token" || r.Header.Get("chatgpt-account-id") != "team-1" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL)
		}
		if !strings.Contains(r.Header.Get("User-Agent"), "Chrome/136") || !strings.Contains(r.Header.Get("Referer"), "/admin/members") {
			t.Error("browser headers missing")
		}
		if r.URL.Query().Get("query") == "denied" {
			http.Error(w, "denied", 403)
			return
		}
		if r.URL.Query().Get("query") == "empty" {
			fmt.Fprint(w, `{"items":[],"total":0}`)
			return
		}
		if r.URL.Query().Get("offset") != "25" || r.URL.Query().Get("limit") != "25" || r.URL.Query().Get("query") != "child+test@example.com" {
			t.Errorf("unexpected paging or search: %s", r.URL)
		}
		fmt.Fprint(w, `{"items":[{"id":"child","email":"child+test@example.com","name":"Child","role":"standard-user","seat_type":"prolite","created_time":"2026-09-27T09:47:53.064930Z","deactivated_time":null,"private_field":"must-not-leak"}],"total":26}`)
	}))
	defer proxy.Close()
	settings := model.DefaultSettings()
	settings.BaseURL, settings.ProxyURL = "http://127.0.0.1:1/backend-api", global.URL
	if err := st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	// Disabled mothers remain available for this read-only operation.
	mother := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "mother", TeamAccountID: "team-1", RotationDisabled: true}, "mother-token", proxy.URL)
	missingProxy := saveTestAdmin(t, st, model.AdminAccountProfile{Label: "missing-proxy", TeamAccountID: "team-2"}, "other-token", "")
	s, err := New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		name, id, query string
		status          int
	}{
		{"first live query", mother.ID, "offset=25&limit=25&query=child%2Btest%40example.com", 200},
		{"second live query", mother.ID, "offset=25&limit=25&query=child%2Btest%40example.com", 200},
		{"empty", mother.ID, "query=empty", 200},
		{"upstream denied", mother.ID, "query=denied", 502},
		{"negative offset", mother.ID, "offset=-1", 400},
		{"bad offset", mother.ID, "offset=foo", 400},
		{"zero limit", mother.ID, "limit=0", 400},
		{"unbounded page", mother.ID, "limit=500", 400},
		{"long search", mother.ID, "query=" + strings.Repeat("a", 321), 400},
		{"missing proxy", missingProxy.ID, "", 400},
		{"missing mother", "unknown", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/admin-accounts/"+tc.id+"/members?"+tc.query, nil)
			r.SetPathValue("id", tc.id)
			w := httptest.NewRecorder()
			s.adminAccountMembers(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("live response is cacheable")
			}
			if strings.Contains(w.Body.String(), "must-not-leak") || strings.Contains(w.Body.String(), "mother-token") {
				t.Fatal("unapproved fields leaked")
			}
			if tc.status == 200 && strings.Contains(tc.name, "live query") {
				var result struct {
					Data struct {
						Items     []workflowMemberForTest `json:"items"`
						Total     int                     `json:"total"`
						Offset    int                     `json:"offset"`
						HasMore   bool                    `json:"has_more"`
						QueriedAt string                  `json:"queried_at"`
					} `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				data := result.Data
				if data.Total != 26 || data.Offset != 25 || data.HasMore || data.QueriedAt == "" || len(data.Items) != 1 || data.Items[0].SeatType != "prolite" || !data.Items[0].Active || data.Items[0].CreatedTime == "" {
					t.Fatalf("unexpected page: %+v", data)
				}
			}
		})
	}
	if globalCalls.Load() != 0 || memberCalls.Load() != 4 {
		t.Fatalf("global requests=%d member requests=%d", globalCalls.Load(), memberCalls.Load())
	}
	if len(st.FreeAccounts()) != 0 || len(st.AdminCapacitySnapshots()) != 0 {
		t.Fatal("member lookup changed local rotation state")
	}
}

type workflowMemberForTest struct {
	SeatType    string `json:"seat_type"`
	Active      bool   `json:"active"`
	CreatedTime string `json:"created_time"`
}
