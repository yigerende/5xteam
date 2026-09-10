package httpapi

import (
	"fmt"
	"sync"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestOAuthProxyLeasesBalanceConcurrentWork(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	settings := model.DefaultSettings()
	settings.OAuthProxyMode = "least_used"
	settings.ProxyURL = "http://global.example:8080"
	if err := dataStore.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if _, err := dataStore.SaveProxy(model.ProxyProfile{Name: fmt.Sprintf("proxy-%d", index), URL: fmt.Sprintf("http://proxy-%d.example:8080", index)}); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{store: dataStore, oauthProxyActive: make(map[string]int)}

	const workerCount = 12
	leases := make(chan *oauthProxyLease, workerCount)
	var wait sync.WaitGroup
	for range workerCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			lease, acquireErr := server.acquireOAuthProxy()
			if acquireErr != nil {
				t.Errorf("acquire lease: %v", acquireErr)
				return
			}
			leases <- lease
		}()
	}
	wait.Wait()
	close(leases)

	counts := make(map[string]int)
	var acquired []*oauthProxyLease
	for lease := range leases {
		counts[lease.url]++
		acquired = append(acquired, lease)
	}
	if len(counts) != 3 {
		t.Fatalf("proxy count = %d, distribution=%v", len(counts), counts)
	}
	for proxyURL, count := range counts {
		if count != 4 {
			t.Fatalf("proxy %s acquired %d jobs, distribution=%v", proxyURL, count, counts)
		}
	}
	for _, lease := range acquired {
		lease.Release()
		lease.Release() // release must be idempotent
	}
	server.oauthProxyMu.Lock()
	remaining := len(server.oauthProxyActive)
	server.oauthProxyMu.Unlock()
	if remaining != 0 {
		t.Fatalf("leases were not released: %+v", server.oauthProxyActive)
	}
}

func TestOAuthProxyGlobalModePinsGlobalProxy(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	settings := model.DefaultSettings()
	settings.OAuthProxyMode = "global"
	settings.ProxyURL = "http://global.example:8080"
	if err := dataStore.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.SaveProxy(model.ProxyProfile{Name: "pool", URL: "http://pool.example:8080"}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: dataStore, oauthProxyActive: make(map[string]int)}
	lease, err := server.acquireOAuthProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.url != settings.ProxyURL {
		t.Fatalf("lease proxy = %q, want %q", lease.url, settings.ProxyURL)
	}
}

func TestSub2CostSnapshotsAreIncrementalAndAttributedToAdmin(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })
	admin, err := dataStore.SaveAdminAccount(model.AdminAccountProfile{Label: "admin", TeamAccountID: "team-1"}, "access-token")
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := dataStore.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "child@example.com", UserID: "user-1"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	account, err = dataStore.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
		item.AdminAccountID = admin.ID
		item.AcceptStatus = "completed"
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: dataStore}
	for _, sample := range []struct {
		downstreamID int64
		total        float64
		want         float64
	}{{10, 5, 5}, {10, 8, 8}, {10, 7, 8}, {20, 2, 10}} {
		account, err = server.saveSub2CostSnapshot(account.ID, admin.ID, sample.downstreamID, sample.total)
		if err != nil {
			t.Fatal(err)
		}
		if account.TotalCostUSD != sample.want {
			t.Fatalf("after %+v total cost = %v, want %v", sample, account.TotalCostUSD, sample.want)
		}
	}
	admins := dataStore.AdminAccounts()
	if len(admins) != 1 || admins[0].TeamRotationChildCount != 1 || admins[0].TeamRotationChildCost != 10 {
		t.Fatalf("admin aggregates = %+v", admins)
	}
}
