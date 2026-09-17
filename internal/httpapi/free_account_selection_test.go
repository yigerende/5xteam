package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestFreeAccountMotherSelection(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mother, err := st.SaveAdminAccount(model.AdminAccountProfile{Label: "Mother 8", RotationDisabled: true}, "fixture-mother-secret")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.SaveAdminAccount(model.AdminAccountProfile{Label: "Mother 18"}, "fixture-other-secret")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := st.SaveAdminAccount(model.AdminAccountProfile{Label: "Empty"}, "fixture-empty-secret")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 27; i++ {
		email := fmt.Sprintf("child-%02d@example.com", i)
		p, _, err := st.SaveImportedFreeAccount(model.FreeAccountProfile{Email: email, UserID: email}, "fixture-child-secret")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
		_, err = st.UpdateFreeAccount(p.ID, func(p *model.FreeAccountProfile) {
			p.AdminAccountID, p.TeamAccountID = mother.ID, "workspace-8"
			p.Sub2AccountID, p.CPAAuthFileName = int64(i+1), email+".json"
			p.InviteStatus, p.OAuthStatus = "completed", "completed"
			if i%3 != 0 {
				p.AcceptStatus = "completed"
			}
			if i%3 == 2 {
				p.RemoveStatus = "completed"
			}
			p.Dead = i == 26
			p.LastError = strings.Repeat("unneeded-detail", 1000)
			if i == 24 {
				p.AdminAccountID, p.TeamAccountID = other.ID, "workspace-18"
			}
			if i == 25 {
				p.AdminAccountID, p.TeamAccountID = "", ""
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteFreeAccount(ids[26]); err != nil {
		t.Fatal(err)
	}
	// A later association must supersede the historical visit to Mother 8.
	if _, err := st.UpdateFreeAccount(ids[23], func(p *model.FreeAccountProfile) {
		p.AdminAccountID, p.TeamAccountID = other.ID, "workspace-18"
	}); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: st}
	for _, tc := range []struct {
		name, id    string
		code, count int
	}{
		{"all_pages_and_states_disabled_mother", mother.ID, 200, 23},
		{"other_mother", other.ID, 200, 2},
		{"empty", empty.ID, 200, 0},
		{"missing", "", 400, 0},
		{"unknown", "does-not-exist", 404, 0},
		{"label_is_not_id", "Mother8", 404, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			server.selectFreeAccountsByAdmin(w, httptest.NewRequest("GET", "/api/free-accounts/select-by-admin?admin_account_id="+tc.id, nil))
			if w.Code != tc.code {
				t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
			}
			if tc.code != 200 {
				return
			}
			var result struct {
				OK   bool `json:"ok"`
				Data struct {
					Items []store.FreeAccountSelection `json:"items"`
					Total int                          `json:"total"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !result.OK || result.Data.Total != tc.count || len(result.Data.Items) != tc.count || result.Data.Items == nil {
				t.Fatalf("unexpected selection: %s", w.Body.String())
			}
			seen := map[string]bool{}
			for _, p := range result.Data.Items {
				if p.AdminAccountID != tc.id || seen[p.ID] || p.ID == ids[25] || p.ID == ids[26] {
					t.Fatalf("incorrect or duplicate child: %+v", p)
				}
				seen[p.ID] = true
				if p.Email == "" || p.TeamAccountID == "" || p.InviteStatus != "completed" || p.OAuthStatus != "completed" || p.AcceptStatus == "" || p.RemoveStatus == "" || p.Sub2AccountID == 0 || p.CPAAuthFileName == "" {
					t.Fatalf("missing bulk eligibility field: %+v", p)
				}
			}
			for _, unwanted := range []string{"fixture-", "access_token", "refresh_token", "encrypted_", "last_error", "unneeded-detail"} {
				if strings.Contains(w.Body.String(), unwanted) {
					t.Fatalf("selection leaked unnecessary data: %s", unwanted)
				}
			}
		})
	}
	before, creds, err := st.FreeAccountCredential(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := st.SelectFreeAccountsByAdmin(context.Background(), mother.ID)
			if err != nil || len(items) != 23 {
				t.Errorf("concurrent read: count=%d err=%v", len(items), err)
			}
		}()
	}
	wg.Wait()
	after, afterCreds, err := st.FreeAccountCredential(ids[0])
	if err != nil || !before.UpdatedAt.Equal(after.UpdatedAt) || creds != afterCreds {
		t.Fatal("selection changed account or credentials", err)
	}
	if _, err := st.SelectFreeAccountsByAdmin(context.Background(), " "); err == nil {
		t.Fatal("empty mother accepted")
	}
	if _, err := st.SelectFreeAccountsByAdmin(context.Background(), "unknown"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unknown mother accepted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.SelectFreeAccountsByAdmin(ctx, mother.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}
