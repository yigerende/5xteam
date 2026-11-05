package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/ejh"
)

func TestEJHOverviewDeltaHTTPAndSavedDataInvalidation(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("overview contacted issuer") })
	card := ejh.Card{OrderNo: "saved", Number: "5555555555554444", CVV: "919", Validity: "1299", Status: "1"}
	if _, err := st.ImportEJHCard(card, true); err != nil {
		t.Fatal(err)
	}
	get := func(query string) map[string]json.RawMessage {
		t.Helper()
		w := httptest.NewRecorder()
		s.ejhOverview(w, httptest.NewRequest("GET", "/api/ejh/overview?"+query, nil))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), card.Number) || strings.Contains(w.Body.String(), `"cvvPassword":"919"`) {
			t.Fatal("overview response failed or exposed secrets")
		}
		var out struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data
	}
	s.ejh.mu.Lock() // A slow upstream job must not block these saved-data reads.
	defer s.ejh.mu.Unlock()
	legacy := get("")
	full := get("format=delta&page_size=1&lifecycle=closed")
	if string(full["mode"]) != `"full"` {
		t.Fatal("missing initial full snapshot")
	}
	var inputs map[string]json.RawMessage
	json.Unmarshal(full["data"], &inputs)
	for key, value := range legacy {
		if key != "revision" && key != "server_time" && string(inputs[key]) != string(value) {
			t.Fatal("financial input changed or filtered", key)
		}
	}
	var revision string
	json.Unmarshal(full["revision"], &revision)
	stable := get("format=delta&since=" + revision)
	if string(stable["mode"]) != `"unchanged"` || stable["data"] != nil || stable["changes"] != nil {
		t.Fatal("unchanged response retransmitted data")
	}
	if err := st.EJHPut("balance", "latest", map[string]any{"at": "updated", "data": map[string]any{"balance": "123.45"}}); err != nil {
		t.Fatal(err)
	}
	delta := get("format=delta&since=" + revision)
	if string(delta["mode"]) != `"delta"` || string(delta["base_revision"]) != string(full["revision"]) {
		t.Fatal("saved change not returned as delta")
	}
	var changes map[string]struct {
		Value json.RawMessage `json:"value"`
	}
	json.Unmarshal(delta["changes"], &changes)
	want := get("")
	if string(changes["balance"].Value) != string(want["balance"]) {
		t.Fatal("balance delta differs from full history")
	}
	if string(delta["source_revision"]) == string(full["source_revision"]) {
		t.Fatal("database revision did not advance")
	}
	if string(get("format=delta&since=missing")["mode"]) != `"full"` {
		t.Fatal("unknown base did not recover")
	}
}

func TestEJHOverviewDeltaPreservesCompleteHistory(t *testing.T) {
	h := &ejhOverviewHistory{}
	cards := []map[string]any{}
	for i := 0; i < 250; i++ {
		cards = append(cards, map[string]any{"id": fmt.Sprint(i), "balance": "10.00", "history": strings.Repeat("complete historical evidence ", 20)})
	}
	before := map[string]any{"cards": cards, "cases": []map[string]any{{"id": "old", "state": "opened"}}, "balance": map[string]string{"amount": "100"}, "server_time": "first"}
	full, err := h.response(before, "db-1", "")
	if err != nil || full["mode"] != "full" {
		t.Fatal(full, err)
	}
	original, _ := json.Marshal(before)
	stable, err := h.response(before, "unrelated-db-write", full["revision"].(string))
	if err != nil || stable["mode"] != "unchanged" {
		t.Fatal(stable, err)
	}
	changedCards := append([]map[string]any{}, cards...)
	changedCards[0] = map[string]any{"id": "0", "balance": "8.00", "history": cards[0]["history"]}
	changedCards = append(changedCards[1:], changedCards[0]) // order preserved too
	after := map[string]any{"cards": changedCards, "cases": []map[string]any{{"id": "new", "state": "returned"}}, "balance": map[string]string{"amount": "99"}, "server_time": "second"}
	delta, err := h.response(after, "db-2", full["revision"].(string))
	if err != nil || delta["mode"] != "delta" {
		t.Fatal(delta, err)
	}
	fullJSON, _ := json.Marshal(full)
	deltaJSON, _ := json.Marshal(delta)
	if len(deltaJSON) > len(fullJSON)/10 {
		t.Fatalf("small change retransmitted history: %d / %d", len(deltaJSON), len(fullJSON))
	}
	changes := delta["changes"].(map[string]any)
	patch := changes["cards"].(map[string]any)["rows"].(map[string]any)
	if len(patch["upsert"].([]json.RawMessage)) != 1 || len(patch["order"].([]string)) != 250 || patch["order"].([]string)[249] != "0" {
		t.Fatal("lost rows or ordering")
	}
	unchanged, _ := json.Marshal(before)
	if string(original) != string(unchanged) {
		t.Fatal("snapshot mutated inputs")
	}
	// Removed fields and duplicate/missing IDs must fall back to replacement.
	delete(after, "balance")
	after["cards"] = []map[string]any{{"id": "duplicate"}, {"id": "duplicate"}}
	reply, err := h.response(after, "db-3", delta["revision"].(string))
	if err != nil || !reflect.DeepEqual(reply["removed"], []string{"balance"}) {
		t.Fatal(reply, err)
	}
	if _, ok := reply["changes"].(map[string]any)["cards"].(map[string]any)["value"]; !ok {
		t.Fatal("duplicate IDs must use full array")
	}
	for i := 4; i < 10; i++ {
		after["balance"] = i
		_, err = h.response(after, fmt.Sprint(i), "")
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(h.snapshots) != 3 {
		t.Fatal("unbounded snapshot cache")
	}
	missing, err := h.response(after, "9", full["revision"].(string))
	if err != nil || missing["mode"] != "full" {
		t.Fatal("evicted base must recover with complete snapshot")
	}
}
