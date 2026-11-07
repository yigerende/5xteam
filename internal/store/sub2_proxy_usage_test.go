package store

import (
	"errors"
	"sync"
	"testing"
)

func TestSub2ProxyUsageDurableQuotaAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	u, err := s.Sub2ProxyUsages("site", map[string]int64{"same-proxy": 2})
	if err != nil || u["same-proxy"].Count != 2 {
		t.Fatal(u, err)
	}
	if err = s.SaveSub2ProxyBindingLimit("site", 3); err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveSub2ProxyBinding("one", "secret AT", "site", "same-proxy", "key-one"); err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveSub2ProxyBinding("two", "other", "site", "same-proxy", "key-two"); !errors.Is(err, ErrSub2ProxyBindingLimit) {
		t.Fatal("pending slot not enforced", err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	u, err = s.Sub2ProxyUsages("site", map[string]int64{"same-proxy": 0})
	if err != nil || u["same-proxy"].Count != 2 || u["same-proxy"].Pending != 1 {
		t.Fatal("reimport or restart reset quota", u, err)
	}
	if raw, err := s.Sub2PushAttempt("one"); err != nil || raw != "secret AT" {
		t.Fatal(raw, err)
	}
	if err = s.CompleteSub2ProxyBinding("one", "created", "key-one", 42); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteSub2ProxyBinding("one", "created", "key-one", 42); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSub2PushAttempt("one"); err != nil {
		t.Fatal(err)
	}
	u, _ = s.Sub2ProxyUsages("site", map[string]int64{"same-proxy": 0})
	a := u["same-proxy"]
	if a.Count != 3 || a.Pending != 0 {
		t.Fatal("success double counted or deletion reset", a)
	}
	if err = s.SetSub2ProxyUsage("site", "same-proxy", 1, a.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.SetSub2ProxyUsage("site", "same-proxy", 0, a.Revision); !errors.Is(err, ErrSub2ProxyUsageChanged) {
		t.Fatal("stale manual edit accepted", err)
	}
	if err = s.ReserveSub2ProxyBinding("two", "two", "site", "same-proxy", "key-two"); err != nil {
		t.Fatal(err)
	}
	if err = s.RejectSub2ProxyBinding("two", "key-two"); err != nil {
		t.Fatal(err)
	}
	u, _ = s.Sub2ProxyUsages("site", nil)
	if u["same-proxy"].Count != 1 || u["same-proxy"].Pending != 0 {
		t.Fatal("rejection consumed count", u)
	}
	var sealed string
	if err = s.db.QueryRow("SELECT encrypted_payload FROM sub2_push_attempts WHERE id='one'").Scan(&sealed); err == nil {
		t.Fatal("completed attempt not cleaned")
	}
}

func TestSub2ProxyUsageConcurrentReservationsAndSites(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, site := range []string{"site-a", "site-b"} {
		if _, err = s.Sub2ProxyUsages(site, map[string]int64{"key": 0}); err != nil {
			t.Fatal(err)
		}
		if err = s.SaveSub2ProxyBindingLimit(site, 1); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); out <- s.ReserveSub2ProxyBinding(id, id, "site-a", "key", id) }(id)
	}
	wg.Wait()
	close(out)
	ok, blocked := 0, 0
	for err := range out {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrSub2ProxyBindingLimit) {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || blocked != 1 {
		t.Fatal(ok, blocked)
	}
	if err = s.ReserveSub2ProxyBinding("other", "other", "site-b", "key", "other"); err != nil {
		t.Fatal("unrelated site blocked", err)
	}
	for _, bad := range []int64{-1, MaxProxyBindingCount + 1} {
		if s.SaveSub2ProxyBindingLimit("site-a", bad) == nil || s.SetSub2ProxyUsage("site-a", "key", bad, 0) == nil {
			t.Fatal("invalid count accepted")
		}
	}
}
