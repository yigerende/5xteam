package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

func randomRegistrationID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) startLocalRegistration(email string) map[string]any {
	id := randomRegistrationID()
	job := map[string]any{"job_id": id, "status": "queued", "state": "queued", "email": email, "logs": []any{}, "result": nil, "error": ""}
	s.registrationMu.Lock()
	s.registrationJobs[id] = job
	s.registrationMu.Unlock()
	go s.runLocalRegistration(id, email)
	return cloneRegistrationJob(job)
}

// startLocalLogin starts the local, protocol-only login flow used to obtain a
// fresh ChatGPT access token. It deliberately has no registration or Codex
// OAuth side effects: success means only that /api/auth/session returned an
// accessToken and that token was persisted to this application's mail account.
func (s *Server) startLocalLogin(email string) map[string]any {
	id := randomRegistrationID()
	job := map[string]any{"job_id": id, "status": "queued", "state": "queued", "email": email, "logs": []any{}, "result": nil, "error": ""}
	s.registrationMu.Lock()
	s.registrationJobs[id] = job
	s.registrationMu.Unlock()
	go s.runLocalLogin(id, email)
	return cloneRegistrationJob(job)
}

func cloneRegistrationJob(src map[string]any) map[string]any {
	dst := map[string]any{}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (s *Server) updateRegistration(id, state, message string) {
	s.registrationMu.Lock()
	defer s.registrationMu.Unlock()
	if j := s.registrationJobs[id]; j != nil {
		j["status"], j["state"] = state, state
		logs, _ := j["logs"].([]any)
		logs = append(logs, map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "level": "info", "step": "protocol", "message": message})
		j["logs"] = logs
	}
}
func (s *Server) finishRegistration(id, state, message string) {
	s.registrationMu.Lock()
	defer s.registrationMu.Unlock()
	if j := s.registrationJobs[id]; j != nil {
		j["status"], j["state"], j["error"] = state, state, func() string {
			if state == "success" {
				return ""
			}
			return message
		}()
		j["result"] = map[string]any{"success": state == "success", "message": message}
	}
}
func (s *Server) localRegistrationStatus(id string) (map[string]any, bool) {
	s.registrationMu.RLock()
	defer s.registrationMu.RUnlock()
	j, ok := s.registrationJobs[id]
	if !ok {
		return nil, false
	}
	return cloneRegistrationJob(j), true
}

type registrationHTTP struct {
	client *http.Client
	jar    http.CookieJar
	proxy  string
	python string
}

func newRegistrationHTTP(settings model.Settings) (*registrationHTTP, error) {
	jar, _ := cookiejar.New(nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(settings.ProxyURL) != "" {
		p, e := url.Parse(settings.ProxyURL)
		if e != nil {
			return nil, e
		}
		tr.Proxy = http.ProxyURL(p)
	}
	timeout := time.Duration(settings.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	python := workflow.FindPython()
	return &registrationHTTP{client: &http.Client{Transport: tr, Timeout: timeout, Jar: jar}, jar: jar, proxy: settings.ProxyURL, python: python}, nil
}
func (h *registrationHTTP) do(ctx context.Context, method, endpoint string, form url.Values, body any) (int, string, map[string]any, error) {
	var rd io.Reader
	if form != nil {
		rd = strings.NewReader(form.Encode())
	} else if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, rd)
	if e != nil {
		return 0, "", nil, e
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/149.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	// Mirror the browser navigation/API headers used by turb's protocol flow.
	// OpenAI's edge rejects the otherwise valid requests when these context
	// headers are absent, often with an immediate HTML 403 challenge page.
	if parsed, parseErr := url.Parse(endpoint); parseErr == nil {
		switch {
		case parsed.Host == "chatgpt.com" && strings.HasPrefix(parsed.Path, "/api/auth/"):
			req.Header.Set("Referer", "https://chatgpt.com/")
			req.Header.Set("Origin", "https://chatgpt.com")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			req.Header.Set("Sec-Fetch-Mode", "cors")
			req.Header.Set("Sec-Fetch-Dest", "empty")
		case parsed.Host == "auth.openai.com" && strings.HasPrefix(parsed.Path, "/api/"):
			req.Header.Set("Referer", "https://auth.openai.com/email-verification")
			req.Header.Set("Origin", "https://auth.openai.com")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			req.Header.Set("Sec-Fetch-Mode", "cors")
			req.Header.Set("Sec-Fetch-Dest", "empty")
		case parsed.Host == "auth.openai.com":
			req.Header.Set("Referer", "https://chatgpt.com/")
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			req.Header.Set("Sec-Fetch-Mode", "navigate")
			req.Header.Set("Sec-Fetch-Dest", "document")
			req.Header.Set("Upgrade-Insecure-Requests", "1")
		}
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.python != "" && strings.HasPrefix(endpoint, "https://") && strings.TrimSpace(h.proxy) != "" {
		return h.browserDo(ctx, req)
	}
	resp, e := h.client.Do(req)
	if e != nil {
		return 0, "", nil, e
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	return resp.StatusCode, resp.Request.URL.String(), data, func() error {
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))[:minLen(len(raw), 500)])
		}
		return nil
	}()
}

// browserDo uses the same curl_cffi Chrome impersonation as turb. The cookie
// jar is serialized in/out on every request so the Go task retains one
// continuous OpenAI session across CSRF, signin, OTP, callback and session.
func (h *registrationHTTP) browserDo(ctx context.Context, req *http.Request) (int, string, map[string]any, error) {
	headers := map[string]string{}
	for key, values := range req.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	cookies := map[string]string{}
	if parsed, err := url.Parse(req.URL.String()); err == nil {
		for _, cookie := range h.jar.Cookies(parsed) {
			cookies[cookie.Name] = cookie.Value
		}
	}
	bodyBytes := []byte{}
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	payload, err := json.Marshal(map[string]any{
		"method": req.Method, "url": req.URL.String(), "headers": headers,
		"cookies": cookies, "body": string(bodyBytes), "proxy": h.proxy,
	})
	if err != nil {
		return 0, "", nil, err
	}
	script := `import sys,json,base64
from curl_cffi import requests
p=json.load(sys.stdin)
s=requests.Session(impersonate="chrome136")
proxy=p.get("proxy") or ""
if proxy:
    s.proxies={"http":proxy,"https":proxy}
for k,v in (p.get("cookies") or {}).items():
    s.cookies.set(k,v)
r=s.request(p["method"],p["url"],headers=p.get("headers",{}),data=p.get("body","") or None,allow_redirects=True,timeout=45)
out=[]
for c in s.cookies.jar:
    out.append({"name":c.name,"value":c.value,"domain":c.domain,"path":c.path})
print(json.dumps({"status":r.status_code,"url":str(r.url),"body":base64.b64encode(r.content).decode("ascii"),"cookies":out},separators=(",",":")))`
	command := exec.CommandContext(ctx, h.python, "-c", script)
	command.Stdin = bytes.NewReader(payload)
	output, err := command.Output()
	if err != nil {
		return 0, "", nil, fmt.Errorf("curl_cffi 浏览器请求失败: %w", err)
	}
	var result struct {
		Status  int    `json:"status"`
		URL     string `json:"url"`
		Body    string `json:"body"`
		Cookies []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
			Path   string `json:"path"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return 0, "", nil, fmt.Errorf("解析 curl_cffi 响应失败: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(result.Body)
	if err != nil {
		return result.Status, result.URL, nil, err
	}
	for _, cookie := range result.Cookies {
		if cookie.Name == "" || cookie.Value == "" {
			continue
		}
		domain := strings.TrimPrefix(cookie.Domain, ".")
		if domain == "" {
			domain = req.URL.Hostname()
		}
		cookieURL := "https://" + domain + "/"
		if parsed, parseErr := url.Parse(cookieURL); parseErr == nil {
			h.jar.SetCookies(parsed, []*http.Cookie{{Name: cookie.Name, Value: cookie.Value, Path: cookie.Path}})
		}
	}
	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	if result.Status >= 400 {
		text := strings.TrimSpace(string(data))
		return result.Status, result.URL, decoded, fmt.Errorf("HTTP %d: %s", result.Status, text[:minLen(len(text), 500)])
	}
	return result.Status, result.URL, decoded, nil
}
func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *Server) runLocalRegistration(id, email string) {
	h, err := newRegistrationHTTP(s.store.Settings())
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	ctx := context.Background()
	s.updateRegistration(id, "running", "初始化 OTP-only 纯协议")
	_, _, providers, err := h.do(ctx, http.MethodGet, "https://chatgpt.com/api/auth/providers", nil, nil)
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	_ = providers
	_, _, csrf, err := h.do(ctx, http.MethodGet, "https://chatgpt.com/api/auth/csrf", nil, nil)
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	csrfToken, _ := csrf["csrfToken"].(string)
	if csrfToken == "" {
		s.finishRegistration(id, "failed", "未取得 CSRF token")
		return
	}
	deviceID := randomRegistrationID()
	if parsed, e := url.Parse("https://chatgpt.com/"); e == nil {
		h.jar.SetCookies(parsed, []*http.Cookie{{Name: "oai-did", Value: deviceID, Path: "/"}})
	}
	q := url.Values{"prompt": {"login"}, "ext-oai-did": {deviceID}, "auth_session_logging_id": {randomRegistrationID()}, "ext-passkey-client-capabilities": {"11111"}, "screen_hint": {"login_or_signup"}, "login_hint": {email}}
	status, _, signin, err := h.do(ctx, http.MethodPost, "https://chatgpt.com/api/auth/signin/openai?"+q.Encode(), url.Values{"callbackUrl": {"https://chatgpt.com/"}, "csrfToken": {csrfToken}, "json": {"true"}}, nil)
	if err != nil || status >= 400 {
		s.finishRegistration(id, "failed", fmt.Sprint(err))
		return
	}
	authURL, _ := signin["url"].(string)
	if authURL == "" {
		s.finishRegistration(id, "failed", "signin 未返回 authorize URL")
		return
	}
	s.updateRegistration(id, "running", "等待邮箱验证码")
	status, finalURL, _, err := h.do(ctx, http.MethodGet, authURL, nil, nil)
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	if status >= 400 || strings.Contains(finalURL, "/create-account/password") || strings.Contains(finalURL, "/api/accounts/user/register") {
		s.finishRegistration(id, "failed", "授权流程落入旧密码注册路径")
		return
	}
	_, creds, credsErr := s.store.MailAccountCredential(email)
	if credsErr != nil {
		s.finishRegistration(id, "failed", credsErr.Error())
		return
	}
	code, err := s.pollLocalOTP(ctx, h, creds.PickupURL)
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	s.updateRegistration(id, "running", "提交邮箱验证码")
	_, _, validated, err := h.do(ctx, http.MethodPost, "https://auth.openai.com/api/accounts/email-otp/validate", nil, map[string]string{"code": code})
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	next := nextRegistrationURL(validated)
	pageType := ""
	if page, ok := validated["page"].(map[string]any); ok {
		pageType, _ = page["type"].(string)
	}
	if pageType == "external_url" || (next != "" && !strings.Contains(next, "about-you") && (strings.Contains(next, "chatgpt.com/api/auth/callback") || strings.Contains(next, "/authorize/continue"))) {
		s.updateRegistration(id, "running", "验证码已通过，完成授权回调")
	} else if strings.Contains(next, "about-you") || next == "" {
		s.updateRegistration(id, "running", "提交账号资料")
		_, _, created, err := h.do(ctx, http.MethodPost, "https://auth.openai.com/api/accounts/create_account", nil, map[string]string{"name": "New User", "birthdate": "1995-01-01"})
		if err != nil {
			s.finishRegistration(id, "failed", err.Error())
			return
		}
		next = nextRegistrationURL(created)
	}
	if next != "" {
		_, _, _, err = h.do(ctx, http.MethodGet, next, nil, nil)
		if err != nil {
			s.finishRegistration(id, "failed", err.Error())
			return
		}
	}
	_ = s.store.UpdateMailAccountStatus(email, "success", "")
	s.finishRegistration(id, "success", "注册成功")
}

func (s *Server) runLocalLogin(id, email string) {
	s.updateRegistration(id, "running", "按 turb 纯协议登录 OpenAI")
	profile, creds, err := s.store.MailAccountCredential(email)
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	result, err := runTurbStyleProtocolLoginWithProgress(context.Background(), email, creds.PickupURL, s.store.Settings().ProxyURL, func(message string) {
		s.updateRegistration(id, "running", message)
	})
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	at, _ := result["access_token"].(string)
	if strings.TrimSpace(at) == "" {
		s.finishRegistration(id, "failed", "纯协议登录未返回 accessToken")
		return
	}
	creds.Email = email
	creds.AccessToken = strings.TrimSpace(at)
	profile.Email = email
	if strings.TrimSpace(profile.Label) == "" {
		profile.Label = email
	}
	if _, err = s.store.SaveMailAccount(profile, creds); err != nil {
		// Older imported records may have no stable profile ID (or an ID from
		// the pre-SQLite format). AT acquisition succeeded already, so recover
		// by upserting the mailbox by its unique email instead of reporting a
		// misleading "mailbox does not exist" failure.
		profile.ID = ""
		profile.Email = email
		if strings.TrimSpace(profile.Label) == "" {
			profile.Label = email
		}
		if _, retryErr := s.store.SaveMailAccount(profile, creds); retryErr != nil {
			s.finishRegistration(id, "failed", retryErr.Error())
			return
		}
	}
	s.finishRegistration(id, "success", "临时 AT 获取成功")
}

// runTurbStyleProtocolLogin executes the complete login/OTP/callback/session
// state machine in one curl_cffi Chrome session, matching turb's pure protocol
// method. It intentionally stops at ChatGPT accessToken and never runs Codex.
func runTurbStyleProtocolLogin(ctx context.Context, email, pickupURL, proxy string) (map[string]any, error) {
	return runTurbStyleProtocolLoginWithProgress(ctx, email, pickupURL, proxy, nil)
}

func runTurbStyleProtocolLoginWithProgress(ctx context.Context, email, pickupURL, proxy string, progress func(string)) (map[string]any, error) {
	python := workflow.FindPython()
	if python == "" {
		return nil, errors.New("未找到 Python，无法运行 curl_cffi 纯协议会话")
	}
	scriptPath := filepath.Join("internal", "protocol_login.py")
	if _, err := os.Stat(scriptPath); err != nil {
		return nil, fmt.Errorf("纯协议脚本不存在: %w", err)
	}
	payload, err := json.Marshal(map[string]string{"email": email, "pickup_url": pickupURL, "proxy": proxy})
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, python, scriptPath)
	// Python inherits the Windows console code page by default. Force UTF-8 so
	// Chinese protocol progress messages are not decoded as mojibake by Go/UI.
	command.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	command.Stdin = bytes.NewReader(payload)
	var output bytes.Buffer
	command.Stdout = &output
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("创建纯协议日志管道失败: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("启动纯协议登录失败: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if progress != nil && line != "" {
				line = strings.TrimSpace(strings.TrimPrefix(line, "[protocol]"))
				if line != "" {
					progress(line)
				}
			}
		}
	}()
	err = command.Wait()
	<-done
	if err != nil {
		return nil, fmt.Errorf("纯协议登录执行失败: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("解析纯协议登录结果失败: %w", err)
	}
	if ok, _ := result["success"].(bool); !ok {
		message, _ := result["error"].(string)
		if message == "" {
			message = "纯协议登录失败"
		}
		return nil, errors.New(message)
	}
	return result, nil
}

func (s *Server) runLocalLoginLegacy(id, email string) {
	h, err := newRegistrationHTTP(s.store.Settings())
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	ctx := context.Background()
	s.updateRegistration(id, "running", "初始化 OpenAI 登录协议")
	// turb creates the device cookie before any auth endpoint is touched. A
	// bare NextAuth csrf request without this context is commonly rejected by
	// the OpenAI edge with an HTML 403 challenge.
	deviceID := randomRegistrationID()
	for _, raw := range []string{"https://chatgpt.com/", "https://auth.openai.com/", "https://sentinel.openai.com/"} {
		if parsed, e := url.Parse(raw); e == nil {
			h.jar.SetCookies(parsed, []*http.Cookie{{Name: "oai-did", Value: deviceID, Path: "/"}})
		}
	}
	// Establish the same anonymous navigation context as turb before asking
	// NextAuth for CSRF. The response is only a bootstrap; no credentials are
	// sent and errors are handled by the authoritative csrf request below.
	_, _, _, _ = h.do(ctx, http.MethodGet, "https://chatgpt.com/login", nil, nil)

	_, _, csrf, err := h.do(ctx, http.MethodGet, "https://chatgpt.com/api/auth/csrf", nil, nil)
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	csrfToken, _ := csrf["csrfToken"].(string)
	if csrfToken == "" {
		s.finishRegistration(id, "failed", "未取得 CSRF token")
		return
	}

	q := url.Values{
		"prompt": {"login"}, "ext-oai-did": {deviceID},
		"auth_session_logging_id":         {randomRegistrationID()},
		"ext-passkey-client-capabilities": {"11111"},
		"screen_hint":                     {"login_or_signup"}, "login_hint": {email},
	}
	status, _, signin, err := h.do(ctx, http.MethodPost, "https://chatgpt.com/api/auth/signin/openai?"+q.Encode(), url.Values{
		"callbackUrl": {"https://chatgpt.com/"}, "csrfToken": {csrfToken}, "json": {"true"},
	}, nil)
	if err != nil || status >= 400 {
		if err == nil {
			err = fmt.Errorf("signin failed: HTTP %d", status)
		}
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	authURL, _ := signin["url"].(string)
	if authURL == "" {
		s.finishRegistration(id, "failed", "signin 未返回 authorize URL")
		return
	}

	s.updateRegistration(id, "running", "等待邮箱验证码")
	status, finalURL, _, err := h.do(ctx, http.MethodGet, authURL, nil, nil)
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	if status >= 400 || strings.Contains(finalURL, "/create-account/password") || strings.Contains(finalURL, "/api/accounts/user/register") {
		s.finishRegistration(id, "failed", "登录流程落入注册路径，当前邮箱可能尚未注册")
		return
	}

	profile, creds, credsErr := s.store.MailAccountCredential(email)
	if credsErr != nil {
		s.finishRegistration(id, "failed", credsErr.Error())
		return
	}
	code, err := s.pollLocalOTP(ctx, h, creds.PickupURL)
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	s.updateRegistration(id, "running", "提交邮箱验证码")
	_, _, validated, err := h.do(ctx, http.MethodPost, "https://auth.openai.com/api/accounts/email-otp/validate", nil, map[string]string{"code": code})
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	next := nextRegistrationURL(validated)
	pageType := ""
	if page, ok := validated["page"].(map[string]any); ok {
		pageType, _ = page["type"].(string)
	}
	if next == "" || strings.Contains(next, "about-you") || pageType == "about_you" || pageType == "about-you" {
		s.finishRegistration(id, "failed", "登录 OTP 已通过，但账号进入资料页，未建立已注册登录态")
		return
	}

	s.updateRegistration(id, "running", "完成 OAuth 回调并获取 ChatGPT AT")
	status, _, sessionInfo, err := h.do(ctx, http.MethodGet, next, nil, nil)
	if err != nil || status >= 400 {
		if err == nil {
			err = fmt.Errorf("OAuth callback failed: HTTP %d", status)
		}
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	// The callback response itself is not the token. As in turb, the token is
	// authoritative only when ChatGPT's NextAuth session endpoint returns it.
	_, _, sessionInfo, err = h.do(ctx, http.MethodGet, "https://chatgpt.com/api/auth/session", nil, nil)
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	at, _ := sessionInfo["accessToken"].(string)
	if strings.TrimSpace(at) == "" {
		s.finishRegistration(id, "failed", "/api/auth/session 响应缺少 accessToken")
		return
	}
	creds.AccessToken = strings.TrimSpace(at)
	creds.Email = email
	profile.Email = email
	if strings.TrimSpace(profile.Label) == "" {
		profile.Label = email
	}
	profile, err = s.store.SaveMailAccount(profile, creds)
	if err != nil {
		s.finishRegistration(id, "failed", err.Error())
		return
	}
	s.finishRegistration(id, "success", "临时 AT 获取成功")
}
func nextRegistrationURL(v map[string]any) string {
	for _, k := range []string{"continue_url", "external_url", "url"} {
		if x, ok := v[k].(string); ok && x != "" {
			return x
		}
	}
	if p, ok := v["page"].(map[string]any); ok {
		for _, k := range []string{"continue_url", "external_url", "url"} {
			if x, ok := p[k].(string); ok && x != "" {
				return x
			}
		}
	}
	return ""
}
func (s *Server) pollLocalOTP(ctx context.Context, h *registrationHTTP, pickup string) (string, error) {
	if strings.TrimSpace(pickup) == "" {
		return "", errors.New("邮箱未配置取件链接")
	}
	u := pickup
	if strings.Contains(u, "?") {
		u += "&json=1"
	} else {
		u += "?json=1"
	}
	for i := 0; i < 40; i++ {
		_, _, d, e := h.do(ctx, http.MethodGet, u, nil, nil)
		if e == nil {
			if c := extractOTP(d, ""); c != "" {
				return c, nil
			}
		}
		time.Sleep(3 * time.Second)
	}
	return "", errors.New("等待邮箱验证码超时")
}

var (
	mailOTPPattern        = regexp.MustCompile(`(?i)(?:code\s*(?:is|:)?|verification\s*code\s*(?:is|:)?|login\s*code\s*(?:is|:)?|验证码|驗證碼|安全代码|認証コード|確認コード|Bestätigungscode|Verifizierungscode)\D{0,80}(\d{6})`)
	mailOTPReversePattern = regexp.MustCompile(`(?i)(\d{4,8})\D{0,80}(?:code|验证码|驗證碼|安全代码|認証コード|確認コード|Bestätigungscode|Verifizierungscode)`)
	mailOTPNumericPattern = regexp.MustCompile(`\b\d{6}\b`)
)

// extractOTP mirrors the robust mailbox extraction used by gpt-account-manager:
// direct code fields first, then recursively flattened JSON, HTML/text bodies,
// and finally context-aware 6-digit candidates. It intentionally remains
// restricted to six digits because OpenAI email OTPs are six digits.
func extractOTP(value map[string]any, raw string) string {
	if value != nil {
		if nested, ok := value["messages"].([]any); ok {
			return extractOTPFromMessageList(nested, raw)
		}
		if nested, ok := value["items"].([]any); ok {
			return extractOTPFromMessageList(nested, raw)
		}
		if data, ok := value["data"].(map[string]any); ok {
			if nested, ok := data["messages"].([]any); ok {
				return extractOTPFromMessageList(nested, raw)
			}
			if nested, ok := data["items"].([]any); ok {
				return extractOTPFromMessageList(nested, raw)
			}
		}
	}
	var direct []string
	var walk func(any, bool)
	walk = func(v any, isDirect bool) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				key := strings.ToLower(strings.TrimSpace(k))
				field := key == "code" || key == "verification_code" || key == "verificationcode" || key == "otp" || key == "email_code" || key == "emailcode" || key == "verify_code" || key == "verifycode" || key == "text" || key == "body" || key == "bodypreview" || key == "body_preview" || key == "content" || key == "message" || key == "subject" || key == "title"
				walk(child, isDirect || field && (key == "code" || key == "verification_code" || key == "verificationcode" || key == "otp" || key == "email_code" || key == "emailcode" || key == "verify_code" || key == "verifycode"))
			}
		case []any:
			for _, child := range x {
				walk(child, isDirect)
			}
		case string:
			text := normalizeMailOTPText(x)
			if text == "" {
				return
			}
			if isDirect {
				direct = append(direct, mailOTPNumericPattern.FindAllString(text, -1)...)
			}
		}
	}
	walk(value, false)
	for i := len(direct) - 1; i >= 0; i-- {
		if len(direct[i]) == 6 {
			return direct[i]
		}
	}
	text := normalizeMailOTPText(raw)
	if value != nil {
		var flattened strings.Builder
		appendFlattenedOTPText(&flattened, value)
		text = normalizeMailOTPText(flattened.String() + "\n" + text)
	}
	if text == "" {
		return ""
	}
	if matches := mailOTPPattern.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		return matches[len(matches)-1][1]
	}
	if matches := mailOTPReversePattern.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		return matches[len(matches)-1][1]
	}
	matches := mailOTPNumericPattern.FindAllString(text, -1)
	if len(matches) > 0 {
		return matches[len(matches)-1]
	}
	return ""
}

func extractOTPFromMessageList(items []any, raw string) string {
	messages := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if message, ok := item.(map[string]any); ok {
			messages = append(messages, message)
		}
	}
	sort.SliceStable(messages, func(i, j int) bool {
		return mailMessageTimestamp(messages[i]) > mailMessageTimestamp(messages[j])
	})
	for _, message := range messages {
		if code := extractOTP(message, ""); code != "" {
			return code
		}
	}
	return extractOTP(nil, raw)
}

func mailMessageTimestamp(message map[string]any) int64 {
	for _, key := range []string{"received_at", "receivedAt", "created_at", "createdAt", "timestamp", "time", "date"} {
		value, ok := message[key]
		if !ok {
			continue
		}
		if number, ok := value.(float64); ok {
			if number > 10000000000 {
				number /= 1000
			}
			return int64(number)
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
			if parsed, err := time.Parse(layout, text); err == nil {
				return parsed.Unix()
			}
		}
	}
	return 0
}

func appendFlattenedOTPText(dst *strings.Builder, value any) {
	switch x := value.(type) {
	case map[string]any:
		for _, child := range x {
			appendFlattenedOTPText(dst, child)
			dst.WriteByte('\n')
		}
	case []any:
		for _, child := range x {
			appendFlattenedOTPText(dst, child)
			dst.WriteByte('\n')
		}
	case string:
		dst.WriteString(x)
		dst.WriteByte('\n')
	}
}

func normalizeMailOTPText(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	// Remove markup/template noise and decode HTML entities before matching.
	text = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	text = regexp.MustCompile(`#[0-9a-fA-F]{6}\b`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}
