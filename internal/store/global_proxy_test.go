package store

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"chatgpt-space-merge/internal/model"
)

func TestGlobalProxyCooldownRecoveryAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	p, err := s.SaveProxy(model.ProxyProfile{Name: "main", URL: "http://main.example:8080"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := model.DefaultSettings()
	cfg.ProxyURL = p.URL
	cfg.ProxyFailover.CooldownSeconds = 10
	cfg.ProxyFailover.TestIntervalSeconds = 2
	if err = s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var changed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, e := s.FailGlobalProxy(p.URL, 0, now)
			if e != nil {
				t.Error(e)
			}
			if ok {
				changed.Add(1)
			}
		}()
	}
	wg.Wait()
	if changed.Load() != 1 {
		t.Fatal("duplicate failures extended cooldown", changed.Load())
	}
	if _, ok, e := s.ReadyGlobalProxy(p.URL, now.Add(time.Second)); ok || e != nil {
		t.Fatal("cooling proxy reused", ok, e)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, e := s.ReadyGlobalProxy(p.URL, now.Add(11*time.Second)); ok || e != nil {
		t.Fatal("restart/expiry bypassed test", ok, e)
	}
	at := now.Add(11 * time.Second)
	for attempt := 1; attempt <= 3; attempt++ {
		jobs, e := s.ClaimGlobalProxyProbes(at)
		if e != nil || len(jobs) != 1 {
			t.Fatal("claim", attempt, jobs, e)
		}
		repeat, e := s.ClaimGlobalProxyProbes(at)
		if e != nil || len(repeat) != 0 {
			t.Fatal("overlapping probes")
		}
		if e = s.CompleteGlobalProxyProbe(jobs[0], false, at); e != nil {
			t.Fatal(e)
		}
		states, _ := s.GlobalProxyHealth([]string{p.URL})
		h := states[p.URL]
		if h.Attempts != attempt {
			t.Fatal(h)
		}
		if attempt < 3 {
			if h.State != "recovering" || h.NextAt != at.Add(2*time.Second).UnixMilli() {
				t.Fatal("interval ignored", h)
			}
		} else if h.State != "cooling" || h.Until != at.Add(10*time.Second).UnixMilli() {
			t.Fatal("failed probes not cooled", h)
		}
		at = at.Add(2 * time.Second)
	}
	at = at.Add(10 * time.Second)
	jobs, e := s.ClaimGlobalProxyProbes(at)
	if e != nil || len(jobs) != 1 {
		t.Fatal(e, jobs)
	}
	old := jobs[0]
	replacement, e := s.ClaimGlobalProxyProbes(at.Add(61 * time.Second))
	if e != nil || len(replacement) != 1 {
		t.Fatal("crashed probe not recovered", e)
	}
	if e = s.CompleteGlobalProxyProbe(old, true, at); e != nil {
		t.Fatal(e)
	}
	if _, ready, _ := s.ReadyGlobalProxy(p.URL, at); ready {
		t.Fatal("stale probe cleared newer claim")
	}
	if e = s.CompleteGlobalProxyProbe(replacement[0], true, at.Add(62*time.Second)); e != nil {
		t.Fatal(e)
	}
	h, ready, e := s.ReadyGlobalProxy(p.URL, at.Add(62*time.Second))
	if e != nil || !ready {
		t.Fatal("success not restored", h, e)
	}
	if _, ok, e := s.FailGlobalProxy(p.URL, 0, at.Add(63*time.Second)); ok || e != nil {
		t.Fatal("late old failure poisoned recovered proxy")
	}
}

func TestGlobalProxyBlankTestsAndEditedCredentials(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, _ := s.SaveProxy(model.ProxyProfile{Name: "main", URL: "http://u:old@main.example:8080"})
	cfg := model.DefaultSettings()
	cfg.ProxyFailover.RecoveryTests = nil
	cfg.ProxyFailover.CooldownSeconds = 10
	s.SaveSettings(cfg)
	now := time.Now()
	s.FailGlobalProxy(p.URL, 0, now)
	if _, ready, _ := s.ReadyGlobalProxy(p.URL, now.Add(9*time.Second)); ready {
		t.Fatal("restored early")
	}
	if _, ready, _ := s.ReadyGlobalProxy(p.URL, now.Add(11*time.Second)); !ready {
		t.Fatal("blank tests did not restore")
	}
	if jobs, e := s.ClaimGlobalProxyProbes(now.Add(11 * time.Second)); e != nil || len(jobs) != 0 {
		t.Fatal("blank setting made probe")
	}
	old := p.URL
	p.URL = "http://u:new@main.example:8080"
	if _, err = s.SaveProxy(p); err != nil {
		t.Fatal(err)
	}
	if _, ready, _ := s.ReadyGlobalProxy(p.URL, now); !ready {
		t.Fatal("new credentials inherited old cooldown")
	}
	if _, changed, e := s.FailGlobalProxy(old, 1, now); changed || e != nil {
		t.Fatal("late result revived removed resource")
	}
	if _, err = s.SaveProxy(model.ProxyProfile{Name: "other", URL: "http://other.example:8080"}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 11, -1} {
		cfg.ProxyFailover.RecoveryTests = &n
		if s.SaveSettings(cfg) == nil {
			t.Fatal("invalid tests accepted", n)
		}
	}
}
