package store

import (
	"chatgpt-space-merge/internal/model"
	"testing"
)

func TestSIWCPushModesPreserveIndependentConnections(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	v := model.DefaultProSettings()
	v.Sub2.URL = "https://ordinary.example.test"
	v.Sub2.Email = "ordinary@example.test"
	v.Sub2.GroupIDs = []int64{1}
	if _, e = s.SaveProSettings(v, "ordinary-secret", ""); e != nil {
		t.Fatal(e)
	}
	siwc, secret, e := s.ProSub2Variant("siwc")
	if e != nil || secret != "" || siwc.URL != "" {
		t.Fatal("new mode inherited ordinary connection")
	}
	siwc.URL = "https://siwc.example.test"
	siwc.Email = "siwc@example.test"
	siwc.GroupIDs = []int64{7}
	siwc.SIWCAuthorizationSource = "sub2api"
	v.Sub2 = siwc
	if _, e = s.SaveProSettings(v, "siwc-secret", ""); e != nil {
		t.Fatal(e)
	}
	ordinary, secret, e := s.ProSub2Variant("")
	if e != nil || secret != "ordinary-secret" || ordinary.URL != "https://ordinary.example.test" || ordinary.GroupIDs[0] != 1 {
		t.Fatal("ordinary settings not retained", e)
	}
	v.Sub2 = ordinary
	if _, e = s.SaveProSettings(v, "", ""); e != nil {
		t.Fatal(e)
	}
	_, secret, _, e = s.ProSettings()
	if e != nil || secret != "ordinary-secret" {
		t.Fatal("mode reused another secret")
	}
	siwc, secret, e = s.ProSub2Variant("siwc")
	if e != nil || secret != "siwc-secret" || siwc.GroupIDs[0] != 7 || siwc.SIWCAuthorizationSource != "sub2api" {
		t.Fatal("SIWC settings not retained", e)
	}
}
