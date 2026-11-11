package sub2

import "testing"

func TestCountGroupOAuthMembership(t *testing.T) {
	accounts := []MonitorAccount{
		{ID: 1, Platform: "openai", Type: "apikey", GroupIDs: []int64{9}},
		{ID: 2, Platform: "openai", Type: "oauth", Status: "disabled", Schedulable: false, GroupIDs: []int64{9}},
		{ID: 3, Platform: "openai", Type: "oauth", Groups: []Group{{ID: 9}}},
		{ID: 4, Platform: "openai", Type: "oauth", GroupIDs: []int64{10}},
	}
	if n, err := CountGroupOAuth(accounts, 9); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if n, err := CountGroupOAuth(accounts[:1], 9); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if n, err := CountGroupOAuth(nil, 9); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	accounts[0].Type = ""
	if _, err := CountGroupOAuth(accounts, 9); err == nil {
		t.Fatal("missing type treated as empty")
	}
}
