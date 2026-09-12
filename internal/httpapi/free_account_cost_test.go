package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func TestSub2UserCostsAreAttributedAndAggregatedByAdmin(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := &Server{store: db}
	admins := make([]model.AdminAccountProfile, 12)
	for i := range admins {
		admins[i], err = db.SaveAdminAccount(model.AdminAccountProfile{Label: fmt.Sprintf("admin-%02d", i), TeamAccountID: fmt.Sprintf("team-%d", i)}, "fixture-token")
		if err != nil {
			t.Fatal(err)
		}
	}
	value := func(v float64) *float64 { return &v }
	createChild := func(name string, admin int, cost *float64, removed bool) model.FreeAccountProfile {
		t.Helper()
		child, _, saveErr := db.SaveImportedFreeAccount(model.FreeAccountProfile{Email: name + "@example.com", UserID: name}, "fixture-token")
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		child, saveErr = db.UpdateFreeAccount(child.ID, func(item *model.FreeAccountProfile) {
			item.AdminAccountID, item.AcceptStatus = admins[admin].ID, "completed"
			item.TotalUserCostUSD = cost
			if removed {
				item.RemoveStatus = "completed"
			}
		})
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		return child
	}
	child := createChild("moving-child", 0, value(8), false)
	_, err = db.UpdateFreeAccount(child.ID, func(item *model.FreeAccountProfile) {
		item.UserCostDownstreamIdentity, item.UserCostDownstreamSnapshot = "42", 8
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, cost := range []float64{10, 10, 9} {
		if _, err := server.saveSub2CostSnapshot(child.ID, admins[0].ID, 42, sub2.AccountCosts{UserCostUSD: value(cost)}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.UpdateFreeAccount(child.ID, func(item *model.FreeAccountProfile) { item.AdminAccountID = admins[1].ID })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.saveSub2CostSnapshot(child.ID, admins[1].ID, 42, sub2.AccountCosts{UserCostUSD: value(12)}); err != nil {
		t.Fatal(err)
	}
	_, err = db.UpdateFreeAccount(child.ID, func(item *model.FreeAccountProfile) { item.RemoveStatus = "completed" })
	if err != nil {
		t.Fatal(err)
	}
	// Removed and legacy records remain included; unavailable user costs do
	// not become a measured zero. Zero itself must remain displayable.
	createChild("legacy-removed", 1, value(3), true)
	createChild("zero", 2, value(0), false)
	createChild("unavailable", 3, nil, false)
	for i := range 12 {
		createChild(fmt.Sprintf("extra-%d", i), 1, value(1), i%2 == 0)
	}
	child, _, err = db.SaveImportedFreeAccount(model.FreeAccountProfile{Email: child.Email, UserID: child.UserID}, "replacement-token")
	if err != nil || child.UserCostByAdmin[admins[0].ID] != 10 || child.UserCostByAdmin[admins[1].ID] != 2 {
		t.Fatalf("reimport lost user cost attribution: %+v, err=%v", child.UserCostByAdmin, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]*float64, len(admins))
	want[0], want[1], want[2] = value(10), value(17), value(0)
	assertCost := func(i int, account model.AdminAccountProfile) {
		t.Helper()
		got := account.TeamRotationChildUserCost
		if account.ID != admins[i].ID || (got == nil) != (want[i] == nil) || got != nil && *got != *want[i] {
			t.Fatalf("admin %d aggregate = %+v, expected user cost %v", i, account, want[i])
		}
	}
	for repeat := 0; repeat < 2; repeat++ {
		for i, account := range db.AdminAccounts() {
			assertCost(i, account)
		}
		for offset := 0; offset < len(admins); offset += 10 {
			page, total, _, err := db.AdminAccountsPage(10, offset)
			if err != nil || total != len(admins) || len(page) != min(10, len(admins)-offset) {
				t.Fatalf("admin page: total=%d items=%d err=%v", total, len(page), err)
			}
			for i, account := range page {
				assertCost(offset+i, account)
			}
		}
	}
}

func TestSub2UserCostSnapshotLifecycle(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	account, _, err := db.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "cost@example.com", UserID: "cost-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db}
	value := func(v float64) *float64 { return &v }
	for _, sample := range []struct {
		identity int64
		user     *float64
		want     *float64
	}{
		{10, nil, nil},
		{10, value(0), value(0)},
		{10, value(5), value(5)},
		{10, value(8), value(8)},
		{10, value(8), value(8)},
		{10, value(7), value(8)},
		{10, nil, value(8)},
		{10, value(9), value(9)},
		{20, nil, value(9)},
		{20, value(2), value(11)},
		{20, value(2), value(11)},
	} {
		account, err = server.saveSub2CostSnapshot(account.ID, "", sample.identity, sub2.AccountCosts{StandardCostUSD: 4, UserCostUSD: sample.user})
		if err != nil {
			t.Fatal(err)
		}
		if (account.TotalUserCostUSD == nil) != (sample.want == nil) || sample.want != nil && *account.TotalUserCostUSD != *sample.want {
			t.Fatalf("after %+v, user cost = %v", sample, account.TotalUserCostUSD)
		}
	}
	// Concurrent, out-of-order refreshes of the same downstream account must
	// retain the high-water mark without counting any snapshot twice.
	var wait sync.WaitGroup
	for i := range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, updateErr := server.saveSub2CostSnapshot(account.ID, "", 20, sub2.AccountCosts{StandardCostUSD: 4, UserCostUSD: value(float64(i))})
			if updateErr != nil {
				t.Error(updateErr)
			}
		}()
	}
	wait.Wait()
	account, _, err = db.SaveImportedFreeAccount(model.FreeAccountProfile{Email: account.Email, UserID: account.UserID}, "new-source-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	account, _, err = db.FreeAccountCredential(account.ID)
	if err != nil || account.TotalUserCostUSD == nil || *account.TotalUserCostUSD != 28 || account.UserCostDownstreamSnapshot != 19 || account.UserCostDownstreamIdentity != "20" || account.TotalCostUSD != 8 {
		t.Fatalf("costs not preserved after reimport/reopen: %+v, err=%v", account, err)
	}
}

func TestSub2CostsRefreshWithQuotaAndBeforeRemoval(t *testing.T) {
	var statsCalls, quotaCalls atomic.Int32
	var failStats atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"admin-token"}}`))
		case "/api/v1/admin/accounts/42/stats":
			statsCalls.Add(1)
			if failStats.Load() {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"code":502,"message":"fixture unavailable"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"summary":{"total_standard_cost":25.1788,"total_user_cost":52.04}}}`))
		case "/api/v1/admin/openai/accounts/42/quota":
			quotaCalls.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"data":{"rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":18000},"secondary_window":{"used_percent":30,"limit_window_seconds":604800}}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	settings := model.Sub2Settings{Provider: "sub2", URL: ts.URL, Email: "admin@example.com"}
	if _, err := db.SaveSub2Settings(settings, "fixture-password"); err != nil {
		t.Fatal(err)
	}
	account, _, err := db.SaveImportedFreeAccount(model.FreeAccountProfile{Email: "cost@example.com", UserID: "cost-user"}, "source-token")
	if err != nil {
		t.Fatal(err)
	}
	account, err = db.UpdateFreeAccount(account.ID, func(item *model.FreeAccountProfile) {
		item.Sub2AccountID, item.PushProvider = 42, "sub2"
		item.AcceptStatus, item.PushStatus = "completed", "completed"
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, sub2: sub2.New()}
	checkCost := func(profile model.FreeAccountProfile) {
		t.Helper()
		if profile.TotalCostUSD != 25.1788 || profile.TotalUserCostUSD == nil || *profile.TotalUserCostUSD != 52.04 {
			t.Fatalf("unexpected saved costs: %+v", profile)
		}
	}
	for _, fail := range []bool{false, true} {
		failStats.Store(fail)
		account, removed, queryErr := server.performFreeAccountQuotaInternal(context.Background(), account.ID, false, false)
		if queryErr != nil || removed || account.QuotaStatus != "completed" {
			t.Fatalf("quota failed: removed=%v, err=%v, account=%+v", removed, queryErr, account)
		}
		checkCost(account)
		checkCost(server.refreshSub2CostBeforeRemoval(context.Background(), account))
	}
	if statsCalls.Load() != 4 || quotaCalls.Load() != 2 {
		t.Fatalf("unexpected request counts: stats=%d quota=%d", statsCalls.Load(), quotaCalls.Load())
	}
	settings.Provider = "cpa"
	if _, err := db.SaveSub2Settings(settings, ""); err != nil {
		t.Fatal(err)
	}
	server.refreshSub2CostBeforeRemoval(context.Background(), account)
	if statsCalls.Load() != 4 {
		t.Fatal("CPA removal requested Sub2 costs")
	}
}
