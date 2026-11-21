package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/sub2"
)

func TestRestockSIWCQuotaCachePreservesUpstreamTime(t *testing.T) {
	checked := time.Now().Add(-15 * time.Second).UTC().Truncate(time.Millisecond)
	reset := time.Now().Add(time.Hour).Unix()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture"}}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/usage") || r.URL.Query().Get("max_age_seconds") != "10" {
			t.Errorf("wrong quota route or freshness: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"code":0,"data":{"checked_at":%q,"rate_limit":{"secondary_window":{"used_percent":80,"limit_window_seconds":604800,"reset_at":%d}}}}`, checked.Format(time.RFC3339Nano), reset)
	}))
	defer upstream.Close()
	p := model.ProSettings{Provider: "sub2", Sub2: model.Sub2Settings{URL: upstream.URL, Email: "fixture@example.invalid", AuthMode: "siwc", QuotaCheckIntervalSeconds: 120}}
	s := &Server{sub2: sub2.New()}
	s.proRestock.quotaBinding = restockBinding(p)
	s.proRestock.quota = map[int64][]restock.Window{}
	s.proRestock.quotaPlans = map[int64]string{}
	c := restock.DefaultConfig()
	c.CheckSeconds, c.QuotaSeconds = 5, 5
	c.QuotaConcurrency = 20
	snap := restock.Snapshot{Binding: restockBinding(p), Accounts: []restock.Account{{ID: 1}}}
	for i := 0; i < 3; i++ {
		s.refreshRestockQuota(c, p, "fixture", snap)
		s.proAutoWG.Wait()
		s.proRestock.mu.Lock()
		windows := s.proRestock.quota[1]
		s.proRestock.quotaNext = time.Time{}
		s.proRestock.mu.Unlock()
		if len(windows) != 1 || !windows[0].At.Equal(checked) || windows[0].Samples != 0 {
			t.Fatalf("cached response gained observation: %+v", windows)
		}
		snap.Accounts[0].Windows = windows
	}
}
