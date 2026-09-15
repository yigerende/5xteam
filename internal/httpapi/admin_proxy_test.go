package httpapi

import (
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

func TestSettingsForAdminUsesDedicatedProxy(t *testing.T) {
	dataStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	proxy, err := dataStore.SaveProxy(model.ProxyProfile{Name: "mother-proxy", URL: "http://proxy.example:8080"})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(dataStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	settings := model.DefaultSettings()
	settings.ProxyURL = "http://global.example:8080"
	got, err := server.settingsForAdmin(settings, model.AdminAccountProfile{ProxyID: proxy.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyURL != proxy.URL {
		t.Fatalf("proxy URL = %q, want dedicated proxy %q", got.ProxyURL, proxy.URL)
	}

	if _, err = server.settingsForAdmin(settings, model.AdminAccountProfile{}); err == nil || !strings.Contains(err.Error(), "专属代理") {
		t.Fatalf("missing dedicated proxy error = %v", err)
	}
	if _, err = server.settingsForAdmin(settings, model.AdminAccountProfile{ProxyID: "missing"}); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("unknown dedicated proxy error = %v", err)
	}
}
