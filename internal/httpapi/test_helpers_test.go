package httpapi

import (
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

// saveTestAdmin creates a mother account with the dedicated proxy required by
// production OpenAI/Team request rules. Tests that do not make upstream calls
// can pass an empty proxy URL.
func saveTestAdmin(t *testing.T, st *store.Store, profile model.AdminAccountProfile, token, proxyURL string) model.AdminAccountProfile {
	t.Helper()
	if proxyURL != "" {
		proxy, err := st.SaveProxy(model.ProxyProfile{Name: profile.Label + "-proxy", URL: proxyURL})
		if err != nil {
			t.Fatal(err)
		}
		profile.ProxyID = proxy.ID
	}
	admin, err := st.SaveAdminAccount(profile, token)
	if err != nil {
		t.Fatal(err)
	}
	return admin
}
