package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func TestRestockQuotaConfiguredConcurrency(t *testing.T) {
	for _, workers := range []int{1, 2, 7, 20, 1000} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			const accounts = 64
			entered := make(chan struct{}, accounts*2)
			release := make(chan struct{})
			var once sync.Once
			var active, peak, calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/login" {
					fmt.Fprint(w, `{"code":0,"data":{"access_token":"fixture"}}`)
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/quota") {
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				n := active.Add(1)
				defer active.Add(-1)
				calls.Add(1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				entered <- struct{}{}
				<-release
				fmt.Fprint(w, `{"code":0,"data":{}}`)
			}))
			defer upstream.Close()
			defer once.Do(func() { close(release) })
			p := model.ProSettings{Provider: "sub2", Sub2: model.Sub2Settings{URL: upstream.URL, Email: "fixture@example.com"}}
			s := &Server{sub2: sub2.New()}
			s.proRestock.quotaBinding = restockBinding(p)
			s.proRestock.quota = map[int64][]restock.Window{}
			s.proRestock.quotaPlans = map[int64]string{}
			snap := restock.Snapshot{Binding: restockBinding(p)}
			for i := 1; i <= accounts; i++ {
				snap.Accounts = append(snap.Accounts, restock.Account{ID: int64(i)})
			}
			c := restock.DefaultConfig()
			c.QuotaConcurrency = workers
			c.CheckSeconds, c.QuotaSeconds = 5, 5
			for round := 0; round < 12; round++ {
				s.refreshRestockQuota(c, p, "fixture", snap)
			}
			for i := 0; i < min(workers, accounts); i++ {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("configured concurrency not reached")
				}
			}
			// Saving a new cadence can make the next run due, but cannot overlap
			// a batch still executing with the previous settings.
			if workers == 20 {
				// Also cross a real five-second wall-clock boundary with the
				// production worker count while upstream requests remain blocked.
				time.Sleep(5100 * time.Millisecond)
			}
			s.proRestock.mu.Lock()
			s.proRestock.quotaNext = time.Time{}
			s.proRestock.mu.Unlock()
			for round := 0; round < 12; round++ {
				s.refreshRestockQuota(c, p, "fixture", snap)
			}
			select {
			case <-entered:
				t.Fatal("exceeded concurrency or overlapped batch")
			case <-time.After(40 * time.Millisecond):
			}
			once.Do(func() { close(release) })
			awaitMoneyWorker(t, s)
			if calls.Load() != accounts || peak.Load() != int32(min(workers, accounts)) {
				t.Fatalf("calls=%d peak=%d", calls.Load(), peak.Load())
			}
		})
	}
}

func TestRestockCadenceAPIPersistence(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.ProRestockConfig()
	if err != nil || c.QuotaConcurrency != 4 {
		t.Fatal("legacy/default concurrency", err)
	}
	s := &Server{store: st}
	s.proRestock.quotaNext = time.Now().Add(time.Hour)
	c.CheckSeconds, c.QuotaSeconds, c.QuotaConcurrency = 1, 10, 9
	c.CooldownSeconds, c.EmergencyCooldownSeconds = 0, 120
	rec := payCall(t, s.saveProRestockConfig, "", "", c)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	got, err := st.ProRestockConfig()
	if err != nil || got.CheckSeconds != 1 || got.QuotaSeconds != 10 || got.QuotaConcurrency != 9 || got.CooldownSeconds != 0 || got.EmergencyCooldownSeconds != 120 {
		t.Fatal("settings not persisted", err, got)
	}
	if !s.proRestock.quotaNext.IsZero() {
		t.Fatal("saved settings did not make next quota run due")
	}
}
