package workflow

import (
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestPrepareIPRoyalOAuthProxy(t *testing.T) {
	for _, tc := range []struct {
		name, input, prefix, suffix string
	}{
		{"plain", "http://user:secret@geo.iproyal.com:12321", "secret_country-us_session-", ""},
		{"region", "socks5h://user:secret_country-de@geo.iproyal.com:12321", "secret_country-de_session-", ""},
		{"existing session", "http://user:secret_country-us_session-old123_lifetime-20m@geo.iproyal.com:12321", "secret_country-us_session-", "_lifetime-20m"},
		{"existing session without region", "http://user:secret_session-old123@geo.iproyal.com:12321", "secret_country-us_session-", ""},
		{"escaped credentials", "http://user%40name:sec%3Aret%40pass_country-us_session-old123@geo.iproyal.com:12321", "sec:ret@pass_country-us_session-", ""},
		{"hostname case", "http://user:secret@GEO.IPROYAL.COM.:12321", "secret_country-us_session-", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PrepareIPRoyalOAuthProxy(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := url.Parse(tc.input)
			after, err := url.Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			if after.Scheme != before.Scheme || after.Host != before.Host || after.User.Username() != before.User.Username() {
				t.Fatal("proxy endpoint or username changed")
			}
			password, _ := after.User.Password()
			want := "^" + regexp.QuoteMeta(tc.prefix) + "[0-9a-f]{8}" + regexp.QuoteMeta(tc.suffix) + "$"
			if ok, _ := regexp.MatchString(want, password); !ok {
				t.Fatalf("unexpected session format: %q", password)
			}
			if strings.Count(password, "_session-") != 1 {
				t.Fatal("duplicate session parameters")
			}
			next, err := PrepareIPRoyalOAuthProxy(got)
			if err != nil || next == got {
				t.Fatal("the next attempt must receive a new session")
			}
		})
	}
}

func TestPrepareIPRoyalOAuthProxyDoesNotModifyOtherProviders(t *testing.T) {
	for _, input := range []string{
		"http://user:secret@proxy.example:8080",
		"http://user-session-123:secret@global.rotgb.711proxy.com:10000",
		"http://user:secret@geo.iproyal.com.example:12321",
		"http://user:secret@static.iproyal.com:12321",
		"http://127.0.0.1:8080",
		"",
	} {
		got, err := PrepareIPRoyalOAuthProxy(input)
		if err != nil || got != input {
			t.Errorf("unrelated proxy was modified: %q", input)
		}
	}
}

func TestPrepareIPRoyalOAuthProxyRequiresCredentials(t *testing.T) {
	for _, input := range []string{"http://geo.iproyal.com:12321", "http://user@geo.iproyal.com:12321", "http://user:@geo.iproyal.com:12321"} {
		if _, err := PrepareIPRoyalOAuthProxy(input); err == nil {
			t.Errorf("accepted missing credentials for %q", input)
		}
	}
}

func TestPrepareIPRoyalOAuthProxyConcurrentTasksAreIsolated(t *testing.T) {
	const jobs = 64
	urls := make(chan string, jobs)
	var wg sync.WaitGroup
	for range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			proxy, err := PrepareIPRoyalOAuthProxy("http://user:secret@geo.iproyal.com:12321")
			if err != nil {
				t.Error(err)
				return
			}
			urls <- proxy
		}()
	}
	wg.Wait()
	close(urls)
	seen := make(map[string]bool)
	for proxy := range urls {
		if seen[proxy] {
			t.Fatal("concurrent OAuth tasks share a session")
		}
		seen[proxy] = true
	}
	if len(seen) != jobs {
		t.Fatalf("created %d sessions, want %d", len(seen), jobs)
	}
}
