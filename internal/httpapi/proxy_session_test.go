package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestVendorProxySettingsRoundTripAndFailover(t *testing.T) {
	s, st, _ := gptPayFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected remote call") })
	s.proAutoCancel()
	profiles := []model.ProxyProfile{}
	for _, v := range []string{"iproyal", "711proxy", "1024proxy"} {
		input := map[string]any{"name": v, "url": "http://fixture:password@" + v + ".example:8080", "vendor": v, "session_minutes": 60, "country": "PH"}
		r := payCall(t, s.createProxy, "", "", input)
		if r.Code != 201 {
			t.Fatalf("response=%s profiles=%+v", r.Body.String(), st.Proxies())
		}
		var body struct {
			Data model.ProxyProfile `json:"data"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, body.Data)
	}
	page, total, err := st.ProxiesPage(50, 0)
	if err != nil || total != 3 || len(page) != 3 {
		t.Fatal(err, total)
	}
	for _, p := range page {
		if p.Vendor == "" || p.Country != "PH" || p.SessionMinutes != 60 {
			t.Fatal("list config lost")
		}
	}
	global := st.Settings()
	global.ProxyURL = profiles[0].URL
	global.ProxyFailover.BackupIDs = []string{profiles[1].ID, profiles[2].ID}
	st.SaveSettings(global)
	main, err := s.acquireOAuthProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer main.Release()
	generated, err := main.sessionURL(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	same, e := main.sessionURL(1, 1)
	if e != nil || same != generated {
		t.Fatal("same lease rotated session", e)
	}
	u, _ := url.Parse(generated)
	password, _ := u.User.Password()
	if !strings.Contains(password, "_country-ph_session-") || !strings.HasSuffix(password, "_lifetime-1h") {
		t.Fatal("primary ignored configuration")
	}
	s.recordLoginProxyFailure(main, contextProxyFailure{})
	backup, err := s.acquireOAuthProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Release()
	if backup.key != profiles[1].URL {
		t.Fatal("wrong backup")
	}
	generated, err = backup.sessionURL(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(generated)
	if !strings.Contains(u.User.Username(), "-region-PH-session-") || !strings.HasSuffix(u.User.Username(), "-sessTime-60") {
		t.Fatal("backup ignored vendor")
	}
	if backup.url != profiles[1].URL {
		t.Fatal("configured URL overwritten")
	}
	// An older client editing only the name must not reset sticky settings.
	r := payCall(t, s.updateProxy, "id", profiles[0].ID, map[string]any{"name": "renamed", "url": profiles[0].URL})
	if r.Code != 200 {
		t.Fatalf("response=%s profiles=%+v", r.Body.String(), st.Proxies())
	}
	listed := httptest.NewRecorder()
	s.listProxies(listed, httptest.NewRequest("GET", "/api/proxies", nil))
	if !strings.Contains(listed.Body.String(), `"vendor":"iproyal"`) {
		t.Fatal("old client removed config")
	}
}

type contextProxyFailure struct{}

func (contextProxyFailure) Error() string { return "curl: (28) Operation timed out" }
