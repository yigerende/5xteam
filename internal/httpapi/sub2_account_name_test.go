package httpapi

import (
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestSub2NameWithMother(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admin, err := st.SaveAdminAccount(model.AdminAccountProfile{Label: "Mother A", Email: "mother@example.com", TeamAccountID: "team-a"}, "token")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	for _, tc := range []struct{ name, adminID, fallback, want string }{
		{"child--09:30", admin.ID, admin.Email, "Mother A|child--09:30"},
		{"child--09:31-重登", admin.ID, admin.Email, "Mother A|child--09:31-重登"},
		{"Mother A|child--09:30", admin.ID, "", "Mother A|child--09:30"},
		{"child--09:30", "deleted", admin.Email, "mother@example.com|child--09:30"},
		{"pro--09:30", admin.ID, "", "Mother A|pro--09:30"},
		{"pro--09:30", "", "", "pro--09:30"},
	} {
		if got := s.sub2NameWithMother(tc.name, tc.adminID, tc.fallback); got != tc.want {
			t.Fatalf("name %q: got %q, want %q", tc.name, got, tc.want)
		}
	}
	admin.Label = "Mother Renamed"
	if _, err := st.SaveAdminAccount(admin, "token"); err != nil {
		t.Fatal(err)
	}
	if got := s.sub2NameWithMother("child--09:32", admin.ID, admin.Email); got != "Mother Renamed|child--09:32" {
		t.Fatal(got)
	}
	other, err := st.SaveAdminAccount(model.AdminAccountProfile{Label: "Mother B", TeamAccountID: "team-b"}, "token")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.sub2NameWithMother("child--09:33", other.ID, admin.Email); got != "Mother B|child--09:33" {
		t.Fatal(got)
	}
	if strings.Contains(cpaAccountName(model.FreeAccountProfile{Email: "child@example.com", AdminAccountID: admin.ID}, false), "|") {
		t.Fatal("CPA naming changed")
	}
}
