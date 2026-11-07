package sub2

import (
	"testing"
	"time"
)

func TestProxyBindingIdentityAndQuotaSelection(t *testing.T) {
	a := ProxyBindingKey(" HTTP ", "Proxy.EXAMPLE.", 8080, "name")
	if a == "" || a != ProxyBindingKey("http", "proxy.example", 8080, "name") {
		t.Fatal("canonical reimport identity changed")
	}
	if a == ProxyBindingKey("http", "proxy.example", 8080, "another-user") {
		t.Fatal("gateway users collapsed")
	}
	if ProxyBindingKey("socks5", "[::1]", 1080, "") != ProxyBindingKey("socks5", "0:0:0:0:0:0:0:1", 1080, "") {
		t.Fatal("IPv6 not normalized")
	}
	if ProxyBindingKey("http", "", 0, "") != "" {
		t.Fatal("incomplete identity accepted")
	}
	zero := 0
	list := []Proxy{{ID: 1, Status: "active", AccountCount: &zero, BindingKey: a, BindingLimit: 2, CumulativeBindings: 2}, {ID: 2, Status: "active", AccountCount: &zero, BindingKey: "second", BindingLimit: 2, CumulativeBindings: 1, PendingBindings: 1}, {ID: 3, Status: "active", AccountCount: &zero, BindingKey: "third", BindingLimit: 2}}
	chosen, _, err := SelectProxy(list, []int64{1, 2, 3}, time.Now())
	if err != nil || chosen.ID != 3 {
		t.Fatal("quota ignored", chosen, err)
	}
	if _, _, err = SelectProxy(list, []int64{1, 2}, time.Now()); err == nil {
		t.Fatal("exhausted proxies accepted")
	}
	list[0].BindingLimit = 0
	if _, _, err = SelectProxy(list, []int64{1}, time.Now()); err != nil {
		t.Fatal("unlimited blocked", err)
	}
}
