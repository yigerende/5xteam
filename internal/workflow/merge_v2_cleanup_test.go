package workflow

import (
	"chatgpt-space-merge/internal/model"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMergeV2RemovedRequiresCompleteUnambiguousLists(t *testing.T) {
	for _, body := range []string{`{}`, `{"items":[null]}`, `{"items":[],"total":1}`, `{"items":[],"has_more":true}`, `{"items":[{"email":"child@example.com","id":"child"}]}`, `{"items":[],"total":-1}`, `{"items":[],"has_more":"false"}`} {
		t.Run(body, func(t *testing.T) {
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer u.Close()
			cfg := model.DefaultSettings()
			cfg.BaseURL = u.URL
			cfg.ProxyURL = u.URL
			c, err := NewClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = c.ConfirmMergeV2Removed(t.Context(), "mother", "team", "child@example.com", "child"); err == nil {
				t.Fatal("incomplete evidence accepted")
			}
		})
	}
	count := 0
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Method != "GET" {
			t.Error("mutation during confirm")
		}
		fmt.Fprint(w, `{"items":[],"total":0}`)
	}))
	defer u.Close()
	cfg := model.DefaultSettings()
	cfg.BaseURL = u.URL
	cfg.ProxyURL = u.URL
	c, _ := NewClient(cfg)
	if err := c.ConfirmMergeV2Removed(t.Context(), "mother", "team", "child@example.com", "child"); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}

func TestMergeV2RemovalRequiresFreshRosterAndMatchesAlias(t *testing.T) {
	for _, mode := range []string{"success_but_present", "lost_response_removed", "wrong_id", "owner"} {
		t.Run(mode, func(t *testing.T) {
			deletes := 0
			removed := false
			u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					deletes++
					if mode == "lost_response_removed" {
						removed = true
						w.WriteHeader(404)
					}
					fmt.Fprint(w, `{"success":true}`)
					return
				}
				q := r.URL.Query()
				if q.Get("limit") != "25" || !q.Has("query") || q.Get("query") != "" {
					t.Error("filtered or oversized roster")
				}
				if strings.HasSuffix(r.URL.Path, "/invites") {
					if q.Get("include_pending") != "true" || q.Get("include_requests") != "true" {
						t.Error("missing invitations")
					}
					fmt.Fprint(w, `{"items":[],"total":0}`)
					return
				}
				if removed {
					fmt.Fprint(w, `{"items":[],"total":0}`)
					return
				}
				id, role := "child", "standard-user"
				if mode == "wrong_id" {
					id = "other"
				}
				if mode == "owner" {
					role = "owner"
				}
				fmt.Fprintf(w, `{"items":[{"id":"row-id","chatgpt_user_id":%q,"role":%q,"email":"child@example.com"}],"total":1}`, id, role)
			}))
			defer u.Close()
			cfg := model.DefaultSettings()
			cfg.BaseURL, cfg.ProxyURL = u.URL, u.URL
			c, _ := NewClient(cfg)
			err := c.RemoveMergeV2Target(t.Context(), "mother", "team", "child@example.com", "child", false)
			if (err == nil) != (mode == "lost_response_removed") {
				t.Fatal(mode, err)
			}
			if (mode == "wrong_id" || mode == "owner") && deletes != 0 {
				t.Fatal("deleted different identity")
			}
		})
	}
}

func TestMergeV2RepeatedPagesCannotProveAbsence(t *testing.T) {
	pages := 0
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		fmt.Fprint(w, `{"items":[{"id":"other","email":"other@example.com"}],"total":3}`)
	}))
	defer u.Close()
	cfg := model.DefaultSettings()
	cfg.BaseURL, cfg.ProxyURL = u.URL, u.URL
	c, _ := NewClient(cfg)
	if err := c.ConfirmMergeV2Removed(t.Context(), "mother", "team", "child@example.com", "child"); err == nil || pages != 2 {
		t.Fatal("duplicate pages trusted", err, pages)
	}
}
