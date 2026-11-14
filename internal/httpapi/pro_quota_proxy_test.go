package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

// Real Go login entry points and subprocess payloads, isolated upstream replies.
func TestProQuotaProxyReusesContinuousLoginAcrossConfigChangeAndRetry(t *testing.T) {
	if workflow.FindPython() == "" {
		t.Skip("Python unavailable")
	}
	for _, vendor := range []string{"iproyal", "711proxy", "1024proxy"} {
		t.Run(vendor, func(t *testing.T) {
			f := newSIWCQuotaFixture(t)
			f.s.proAutoHooks = nil
			p, c, err := f.s.store.MailAccountCredential(f.email)
			if err != nil {
				t.Fatal(err)
			}
			c.GptPassword, c.TotpSecret = "test-password", "JBSWY3DPEHPK3PXP"
			if _, err = f.s.store.SaveMailAccount(p, c); err != nil {
				t.Fatal(err)
			}
			p, err = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.ID = "quota-pin-flow" })
			if err != nil {
				t.Fatal(err)
			}
			proxy, err := f.s.store.SaveProxy(model.ProxyProfile{Name: "original", URL: "http://fixture:private@original.example:8080", ProxySessionConfig: model.ProxySessionConfig{Vendor: vendor, Country: "PH", SessionMinutes: 60}})
			if err != nil {
				t.Fatal(err)
			}
			settings := f.s.store.Settings()
			settings.ProxyURL = proxy.URL
			if err = f.s.store.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			tmp := t.TempDir()
			t.Chdir(tmp)
			if err = os.Mkdir("internal", 0700); err != nil {
				t.Fatal(err)
			}
			write := func(name, source string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join("internal", name), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("protocol_pro_auto.py", `import json,sys,pathlib
p=json.loads(sys.stdin.readline())
pathlib.Path('login-proxy.txt').write_text(p['proxy'])
print(json.dumps({'success':True}),flush=True)
`)
			if err = f.s.runProContinuousSession(context.Background(), f.email, nil); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile("login-proxy.txt")
			if err != nil || string(original) == proxy.URL {
				t.Fatal("continuous login did not generate vendor session", err)
			}
			// Editing or removing the current primary must not move this flow.
			settings.ProxyURL = "http://changed.example:9999"
			if err = f.s.store.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(proAutoToken(f.email, "new"))
			write("protocol_codex_oauth.py", `import json,sys,pathlib
p=json.loads(sys.stdin.readline())
assert p['proxy']==pathlib.Path('login-proxy.txt').read_text(), 'proxy session changed'
assert p['credential_mode']=='chatgpt_at'
with pathlib.Path('quota-proxy.txt').open('a') as f:f.write(p['proxy']+'\n')
if not pathlib.Path('retried').exists():
 pathlib.Path('retried').write_text('1')
 print(json.dumps({'success':False,'error':'fixture network timeout'}),flush=True)
else:print(json.dumps({'success':True,'access_token':`+string(encoded)+`}),flush=True)
`)
			if _, err = f.query(); err == nil {
				t.Fatal("expected first isolated login failure")
			}
			// Make only the configured next quota retry due; do not restart payment.
			if err = f.s.store.UpdateMailAccountSIWCWeb(f.email, "app", func(c *model.SIWCCredentials) error { c.WebQuota.RetryAt = c.WebQuota.LoginAt; return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err = f.query(); err != nil {
				t.Fatal(err)
			}
			if _, err = f.query(); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile("quota-proxy.txt")
			lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), "\n")
			if len(lines) != 2 || lines[0] != string(original) || lines[1] != string(original) {
				t.Fatal("quota retry/poll rotated or repeated login")
			}
			if f.syncs.Load() != 1 || f.refreshes.Load() != 1 {
				t.Fatal("quota reimported account or repeated sync")
			}
			ctx := context.WithValue(context.Background(), proSIWCQuotaLoginKey{}, &proSIWCQuotaLoginOptions{FlowID: "old-flow"})
			if _, err = f.s.executeOpenAILoginContext(ctx, f.email, "chatgpt_at", nil, nil); err == nil {
				t.Fatal("stale flow logged in")
			}
			// A newer/legacy flow without a saved session must not inherit the
			// previous flow's pin or lose its existing login fallback.
			_, err = f.s.store.UpdateProAccount(f.email, func(p *model.MailAccountProfile) { p.ProAuto.ID = "legacy-without-pin" })
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.store.UpdateMailAccountSIWCWeb(f.email, "app", func(c *model.SIWCCredentials) error { c.WebAccessToken = ""; c.WebQuota = nil; return nil }); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile("login-proxy.txt", []byte(settings.ProxyURL), 0600); err != nil {
				t.Fatal(err)
			}
			write("protocol_proxy_probe.py", "import json\nprint(json.dumps({'ok':True,'auth_status':200}))\n")
			if _, err = f.query(); err != nil {
				t.Fatal("legacy login compatibility failed", err)
			}
		})
	}
}
