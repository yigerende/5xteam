package workflow

import (
	"chatgpt-space-merge/internal/model"
	"net/url"
	"strings"
	"testing"
)

func TestVendorProxySessions(t *testing.T) {
	for _, vendor := range []string{"iproyal", "cliproxy", "1024proxy", "711proxy", "other"} {
		t.Run(vendor, func(t *testing.T) {
			user, password := "fixture", "s:e@cret"
			switch vendor {
			case "iproyal":
				password += "_country-us_session-old_lifetime-15m"
			case "cliproxy", "1024proxy":
				user += "-region-US-sid-old-t-15"
			case "711proxy":
				user += "-zone-custom-region-US-session-12345678-sessTime-15-sessAuto-1"
			case "other":
				user += "-{country}-{session}-{minutes}"
			}
			u := url.URL{Scheme: "socks5h", Host: "[2001:db8::1]:8080", User: url.UserPassword(user, password)}
			cfg := model.ProxySessionConfig{Vendor: vendor}
			a, err := PrepareVendorProxy(u.String(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			b, err := PrepareVendorProxy(u.String(), cfg)
			if err != nil || a == b {
				t.Fatal("sessions were shared", err)
			}
			parsed, _ := url.Parse(a)
			pass, _ := parsed.User.Password()
			name := parsed.User.Username()
			if parsed.Host != u.Host || parsed.Scheme != u.Scheme {
				t.Fatal("changed endpoint")
			}
			switch vendor {
			case "iproyal":
				if name != "fixture" || !strings.HasPrefix(pass, "s:e@cret_country-ph_session-") || !strings.HasSuffix(pass, "_lifetime-1h") {
					t.Fatal("royal settings")
				}
			case "cliproxy", "1024proxy":
				if pass != password || !strings.HasPrefix(name, "fixture-region-PH-sid-") || !strings.HasSuffix(name, "-t-60") {
					t.Fatal("CLI settings")
				}
			case "711proxy":
				if pass != password || !strings.HasPrefix(name, "fixture-zone-custom-region-PH-session-") || !strings.HasSuffix(name, "-sessTime-60-sessAuto-1") {
					t.Fatal("711 settings")
				}
			case "other":
				if pass != password || !strings.HasPrefix(name, "fixture-PH-") || !strings.HasSuffix(name, "-60") {
					t.Fatal("custom settings")
				}
			}
			if strings.Contains(a, "old") || strings.Contains(name, "region-US") {
				t.Fatal("old session options retained")
			}
		})
	}
}

func TestVendorProxyValidationAndLegacy(t *testing.T) {
	for _, c := range []model.ProxySessionConfig{{Vendor: "unknown"}, {Vendor: "711proxy", SessionMinutes: 4}, {Vendor: "1024proxy", SessionMinutes: 121}, {Vendor: "iproyal", Country: "USA"}, {Vendor: "other"}} {
		if _, err := PrepareVendorProxy("http://user:password@proxy.test:8080", c); err == nil {
			t.Fatal("invalid options accepted", c)
		}
	}
	if _, err := PrepareVendorProxy("http://proxy.test:8080", model.ProxySessionConfig{Vendor: "iproyal"}); err == nil {
		t.Fatal("missing credentials")
	}
	const raw = "http://user:password@static.test:8080"
	if got, err := PrepareVendorProxy(raw, model.ProxySessionConfig{}); err != nil || got != raw {
		t.Fatal("legacy changed", err)
	}
}
