package workflow

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestRequestObserverPreservesProtocolAndFailures(t *testing.T) {
	for _, code := range []int{200, 403, 429, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/accounts/transfer" || r.Header.Get("Authorization") != "Bearer fixture-token" || !strings.Contains(r.Header.Get("User-Agent"), "Chrome/136") {
					t.Error("observer changed request")
				}
				// Exceed coarse Windows clock resolution when checking elapsed time.
				time.Sleep(20 * time.Millisecond)
				w.WriteHeader(code)
				fmt.Fprint(w, `{"detail":"fixture detail"}`)
			}))
			defer remote.Close()
			settings := model.DefaultSettings()
			settings.BaseURL = remote.URL
			client, err := NewClient(settings)
			if err != nil {
				t.Fatal(err)
			}
			var records []RequestObservation
			ctx := WithRequestObserver(t.Context(), func(record RequestObservation) { records = append(records, record) })
			result, err := client.Transfer(ctx, "fixture-token", "team")
			if (err == nil) != (code == 200) || result.StatusCode != code || len(records) != 2 {
				t.Fatal("protocol/diagnostics mismatch")
			}
			if records[0].Phase != "start" || records[1].Phase != "end" || records[1].StatusCode != code || !strings.Contains(string(records[1].Body), "fixture detail") || records[1].Duration <= 0 {
				t.Fatalf("missing diagnostics: start=%q end=%q status=%d body=%q duration=%s", records[0].Phase, records[1].Phase, records[1].StatusCode, records[1].Body, records[1].Duration)
			}
			if records[0].Headers["Authorization"] != nil {
				t.Fatal("authorization leaked")
			}
			if records[0].Payload.(map[string]any)["target_account_id"] != "team" {
				t.Fatal("wrong body")
			}
		})
	}
}

func TestRequestObserverCapturesNetworkError(t *testing.T) {
	settings := model.DefaultSettings()
	settings.BaseURL = "http://127.0.0.1:1"
	client, err := NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	var records []RequestObservation
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ctx = WithRequestObserver(ctx, func(record RequestObservation) { records = append(records, record) })
	if _, err = client.Transfer(ctx, "fixture-token", "team"); err == nil {
		t.Fatal("cancelled request succeeded")
	}
	if len(records) != 2 || records[1].Err == nil || records[1].StatusCode != 0 {
		t.Fatal("missing network diagnostics")
	}
}
