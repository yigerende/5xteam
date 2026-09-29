package workflow

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestSeatRecoveryHeldPaginationAndExactIdentity(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		q := r.URL.Query()
		if q.Get("active_vacancy_hold_seat_type") != "prolite" || q.Get("limit") != "25" || q.Get("query") != "child@example.com" {
			t.Error("wrong held filter")
		}
		items := []any{}
		offset, _ := strconv.Atoi(q.Get("offset"))
		for i := offset; i < min(offset+25, 26); i++ {
			email := "child+other@example.com"
			if i == 25 {
				email = "CHILD@example.com"
			}
			items = append(items, map[string]any{"id": "user-" + strconv.Itoa(i), "email": email, "seat_type": "prolite", "reclaimable_seat_type": "prolite", "deactivated_time": "2026-09-29T00:00:00Z"})
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items, "total": 26})
	}))
	defer srv.Close()
	settings := model.DefaultSettings()
	settings.BaseURL = srv.URL
	c, err := NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	items, err := c.HeldSeatMembers(t.Context(), "at", "team", "child@example.com")
	if err != nil || pages != 2 || len(items) != 1 || items[0].ID != "user-25" {
		t.Fatalf("pagination or exact match: pages=%d items=%v err=%v", pages, items, err)
	}
}

func TestSeatRecoveryCapacityRejectsEstimatesAndMissingFields(t *testing.T) {
	for _, body := range []string{`{"items":[]}`, `{"seat_capacity":[{"type":"default","paid":1,"available":1},{"type":"prolite","paid":5,"held":5,"available":0}]}`, `{"seat_capacity":[{"type":"default","paid":1,"held":0,"available":-1},{"type":"prolite","paid":5,"held":5,"available":0}]}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/subscriptions" {
				t.Error("attempted fallback capacity estimate")
			}
			w.Write([]byte(body))
		}))
		settings := model.DefaultSettings()
		settings.BaseURL = srv.URL
		c, err := NewClient(settings)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.RecoverySeatCapacity(t.Context(), "at", "team"); err == nil {
			t.Fatal("accepted incomplete capacity")
		}
		srv.Close()
	}
}
