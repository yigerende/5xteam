package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var smsCodeRE = regexp.MustCompile(`\b\d{4,8}\b`)

func (s *Server) listSMSProviders(w http.ResponseWriter, _ *http.Request) {
	writeAPI(w, http.StatusOK, map[string]any{"providers": s.store.SMSProviders()}, "")
}

func (s *Server) listSMSPhones(w http.ResponseWriter, r *http.Request) {
	limit, offset := 500, 0
	page := s.parsePagination(r)
	if paginationRequested(r) {
		limit, offset = page.Limit, page.Offset
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
		offset = v
	}
	phones, stats, total, err := s.store.SMSPhones(r.URL.Query().Get("provider"), r.URL.Query().Get("state"), limit, offset)
	if err != nil {
		writeAPI(w, 500, nil, "读取号源失败: "+err.Error())
		return
	}
	data := map[string]any{"phones": phones, "items": phones, "stats": stats, "total": total}
	if paginationRequested(r) {
		data["page"], data["page_size"] = page.Page, page.PageSize
	}
	writeAPI(w, 200, data, "")
}

type smsImportInput struct {
	Provider    string `json:"provider"`
	Text        string `json:"text"`
	MaxBindings int    `json:"max_bindings"`
	Note        string `json:"note"`
}

func (s *Server) importSMSPhones(w http.ResponseWriter, r *http.Request) {
	var in smsImportInput
	if err := decodeJSON(w, r, &in, 4<<20); err != nil {
		return
	}
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	if in.Provider == "" {
		in.Provider = "generic"
	}
	if in.MaxBindings < 1 {
		in.MaxBindings = 3
	}
	if in.MaxBindings > 100 {
		in.MaxBindings = 100
	}
	lines := strings.Split(strings.ReplaceAll(in.Text, "\r\n", "\n"), "\n")
	added, skipped := 0, 0
	errs := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		phone, card, apiURL, lease, err := s.redeemSMSLine(r.Context(), in.Provider, line)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		ok, err := s.store.AddSMSPhone(in.Provider, phone, card, apiURL, lease, in.Note, in.MaxBindings)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if ok {
			added++
		} else {
			skipped++
		}
	}
	writeAPI(w, 200, map[string]any{"added": added, "skipped": skipped, "errors": errs}, "")
}

func (s *Server) redeemSMSLine(ctx context.Context, provider, line string) (string, string, string, string, error) {
	if provider == "generic" {
		parts := regexp.MustCompile(`-{2,}`).Split(line, 2)
		if len(parts) != 2 {
			return "", "", "", "", errors.New("格式应为 手机号----接码 URL")
		}
		phone := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(parts[0]), " ", ""), "-", "")
		if phone == "" || !strings.HasPrefix(strings.TrimSpace(parts[1]), "http") {
			return "", "", "", "", errors.New("手机号或接码 URL 无效")
		}
		return phone, "", strings.TrimSpace(parts[1]), "", nil
	}
	client := &http.Client{Timeout: 25 * time.Second}
	var endpoint string
	var payload any
	switch provider {
	case "chongpt":
		endpoint = "https://chongpt.xyz/api/public/cdk/verify"
		payload = map[string]any{"code": strings.TrimSpace(line)}
	case "chong10666":
		endpoint = "https://chong.10666.xyz/api/sms/card/verify"
		payload = map[string]any{"cardCode": strings.TrimSpace(line)}
	default:
		return "", "", "", "", fmt.Errorf("不支持的平台: %s", provider)
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return "", "", "", "", fmt.Errorf("平台返回非 JSON")
	}
	if provider == "chongpt" {
		if ok, _ := m["valid"].(bool); !ok {
			return "", "", "", "", fmt.Errorf("卡密无效: %v", m["message"])
		}
		sms, _ := m["sms"].(map[string]any)
		phone := fmt.Sprint(sms["phoneNumber"])
		if phone == "" || phone == "<nil>" {
			return "", "", "", "", errors.New("卡密未返回手机号")
		}
		return phone, strings.TrimSpace(line), "", fmt.Sprint(sms["leaseExpiresAt"]), nil
	}
	if ok, _ := m["ok"].(bool); !ok {
		return "", "", "", "", fmt.Errorf("卡密无效: %v", m["message"])
	}
	card, _ := m["card"].(map[string]any)
	digits := regexp.MustCompile(`\D+`).ReplaceAllString(fmt.Sprint(card["phone"]), "")
	if digits == "" {
		return "", "", "", "", errors.New("卡密未返回手机号")
	}
	return "+" + digits, strings.TrimSpace(line), "", "", nil
}

func (s *Server) updateSMSPhone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID          string `json:"id"`
		MaxBindings int    `json:"max_bindings"`
		Status      string `json:"status"`
		Note        string `json:"note"`
	}
	if err := decodeJSON(w, r, &in, 1<<20); err != nil {
		return
	}
	fields := map[string]any{}
	if in.MaxBindings > 0 {
		fields["max_bindings"] = in.MaxBindings
	}
	if in.Status != "" {
		fields["status"] = in.Status
	}
	if in.Note != "" {
		fields["note"] = in.Note
	}
	if err := s.store.UpdateSMSPhone(in.ID, fields); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"updated": true}, "")
}
func (s *Server) deleteSMSPhones(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(w, r, &in, 1<<20); err != nil {
		return
	}
	n, e := s.store.DeleteSMSPhones(in.IDs)
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"deleted": n}, "")
}

func (s *Server) fetchSMSCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(w, r, &in, 1<<20); err != nil {
		return
	}
	p, e := s.store.SMSPhone(in.ID)
	if e != nil {
		writeAPI(w, 404, nil, e.Error())
		return
	}
	result := s.fetchSMSProvider(r.Context(), p)
	if result["code"] != "" {
		_ = s.store.SaveSMSCode(in.ID, fmt.Sprint(result["code"]), fmt.Sprint(result["lease_expires_at"]))
	}
	writeAPI(w, 200, result, "")
}
func (s *Server) fetchSMSProvider(ctx context.Context, p map[string]any) map[string]any {
	provider := fmt.Sprint(p["provider"])
	c := &http.Client{Timeout: 20 * time.Second}
	endpoint := ""
	payload := map[string]any{}
	card := fmt.Sprint(p["card_code"])
	switch provider {
	case "chongpt":
		endpoint = "https://chongpt.xyz/api/public/sms/session"
		payload["code"] = card
	case "chong10666":
		endpoint = "https://chong.10666.xyz/api/sms/fetch"
		payload["cardCode"] = card
	case "generic":
		endpoint = fmt.Sprint(p["api_url"])
		endpoint = strings.ReplaceAll(endpoint, "{phone}", url.QueryEscape(fmt.Sprint(p["phone_number"])))
	default:
		return map[string]any{"ok": false, "found": false, "code": "", "message": "该平台暂不支持本地取码"}
	}
	var req *http.Request
	var err error
	if provider == "generic" {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	} else {
		raw, _ := json.Marshal(payload)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
	}
	if err != nil {
		return map[string]any{"ok": false, "found": false, "code": "", "message": err.Error()}
	}
	resp, err := c.Do(req)
	if err != nil {
		return map[string]any{"ok": false, "found": false, "code": "", "message": err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var obj any
	_ = json.Unmarshal(raw, &obj)
	code := extractSMSCode(obj)
	msg := string(raw)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	found := code != ""
	if provider == "chongpt" {
		if m, ok := obj.(map[string]any); ok {
			found = m["status"] == "received" && fmt.Sprint(m["verificationCode"]) != "<nil>" && fmt.Sprint(m["verificationCode"]) != ""
			if found {
				code = fmt.Sprint(m["verificationCode"])
			}
		}
	}
	if provider == "chong10666" {
		if m, ok := obj.(map[string]any); ok {
			found, _ = m["hasSms"].(bool)
			if fmt.Sprint(m["code"]) != "<nil>" && fmt.Sprint(m["code"]) != "" {
				code = fmt.Sprint(m["code"])
			}
		}
	}
	return map[string]any{"ok": true, "found": found, "code": code, "message": msg, "lease_expires_at": "", "raw": obj}
}
func extractSMSCode(v any) string {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range []string{"verification_code", "verificationCode", "sms_code", "smsCode", "otp", "verify_code", "code"} {
			if z, ok := x[k]; ok {
				m := smsCodeRE.FindString(strings.TrimSpace(fmt.Sprint(z)))
				if m != "" && len(strings.TrimSpace(fmt.Sprint(z))) <= 8 {
					return m
				}
			}
		}
		for _, z := range x {
			if m := extractSMSCode(z); m != "" {
				return m
			}
		}
	case []any:
		for _, z := range x {
			if m := extractSMSCode(z); m != "" {
				return m
			}
		}
	}
	return ""
}

func (s *Server) bindSMSPhone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID    string `json:"id"`
		Email string `json:"gpt_email"`
	}
	if err := decodeJSON(w, r, &in, 1<<20); err != nil {
		return
	}
	if err := s.store.BindSMSPhone(in.ID, in.Email); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"bound": true}, "")
}
func (s *Server) unbindSMSPhone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"gpt_email"`
	}
	if err := decodeJSON(w, r, &in, 1<<20); err != nil {
		return
	}
	if err := s.store.UnbindSMSPhone(in.Email); err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"unbound": true}, "")
}

func (s *Server) getSMSPlatformConfig(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider == "" {
		writeAPI(w, 400, nil, "缺少 provider")
		return
	}
	cfg, _ := s.store.SMSConfig(provider)
	writeAPI(w, 200, map[string]any{"config": cfg}, "")
}
func (s *Server) saveSMSPlatformConfig(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if err := decodeJSON(w, r, &in, 2<<20); err != nil {
		return
	}
	provider := strings.TrimSpace(fmt.Sprint(in["provider"]))
	enabled, _ := in["enabled"].(bool)
	if provider == "" {
		writeAPI(w, 400, nil, "缺少 provider")
		return
	}
	if err := s.store.SaveSMSConfig(provider, in, enabled); err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	cfg, _ := s.store.SMSConfig(provider)
	writeAPI(w, 200, map[string]any{"config": cfg}, "")
}
func (s *Server) getSMSPlatformBalance(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	cfg, _ := s.store.SMSConfigRaw(provider)
	if provider == "hero_sms" {
		if strings.TrimSpace(fmt.Sprint(cfg["api_key"])) == "" {
			writeAPI(w, 400, nil, "未配置 API Key")
			return
		}
		base := strings.TrimSpace(fmt.Sprint(cfg["base_url"]))
		if base == "" {
			base = "https://hero-sms.com/stubs/handler_api.php"
		}
		u, _ := url.Parse(base)
		q := u.Query()
		q.Set("api_key", fmt.Sprint(cfg["api_key"]))
		q.Set("action", "getBalance")
		u.RawQuery = q.Encode()
		resp, err := (&http.Client{Timeout: 20 * time.Second}).Get(u.String())
		if err != nil {
			writeAPI(w, 502, nil, "余额查询失败: "+err.Error())
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		text := strings.TrimSpace(string(raw))
		if strings.HasPrefix(text, "ACCESS_BALANCE:") {
			writeAPI(w, 200, map[string]any{"success": true, "balance": strings.TrimPrefix(text, "ACCESS_BALANCE:")}, "")
			return
		}
		writeAPI(w, 502, nil, "余额查询失败: "+text)
	}
	cdks := strings.TrimSpace(fmt.Sprint(cfg["cdks"]))
	n := 0
	if cdks != "" {
		n = len(strings.FieldsFunc(cdks, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }))
	}
	writeAPI(w, 200, map[string]any{"success": true, "balance": fmt.Sprintf("%d 张卡", n)}, "")
}
func (s *Server) testSMSPlatform(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
	}
	if err := decodeJSON(w, r, &in, 1<<20); err != nil {
		return
	}
	cfg, _ := s.store.SMSConfigRaw(in.Provider)
	if in.Provider == "hero_sms" {
		if strings.TrimSpace(fmt.Sprint(cfg["api_key"])) == "" {
			writeAPI(w, 400, nil, "未配置 API Key")
			return
		}
		base := strings.TrimSpace(fmt.Sprint(cfg["base_url"]))
		if base == "" {
			base = "https://hero-sms.com/stubs/handler_api.php"
		}
		u, _ := url.Parse(base)
		q := u.Query()
		q.Set("api_key", fmt.Sprint(cfg["api_key"]))
		q.Set("action", "getNumber")
		q.Set("service", firstSMSConfig(cfg, "service", "dr"))
		q.Set("country", firstSMSConfig(cfg, "country", "52"))
		q.Set("maxPrice", firstSMSConfig(cfg, "max_price", "1"))
		u.RawQuery = q.Encode()
		resp, err := (&http.Client{Timeout: 25 * time.Second}).Get(u.String())
		if err != nil {
			writeAPI(w, 502, nil, "测试申请失败: "+err.Error())
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		text := strings.TrimSpace(string(raw))
		if strings.HasPrefix(text, "ACCESS_NUMBER:") {
			parts := strings.Split(text, ":")
			if len(parts) >= 3 {
				writeAPI(w, 200, map[string]any{"success": true, "message": "测试申请成功", "phone": parts[2], "activation_id": parts[1]}, "")
				return
			}
		}
		writeAPI(w, 502, nil, "测试申请失败: "+text)
		return
	}
	writeAPI(w, 200, map[string]any{"success": true, "message": "平台配置已保存，可在实际登录流程中按需申请号码"}, "")
}

// getSMSPlatformHistory returns the provider activation history.  Hero-SMS
// exposes this through its REST API (GET /activations/history), while the
// legacy handler_api.php endpoint remains the source for balance/number
// operations.  The API key is only read server-side and is never returned.
func (s *Server) getSMSPlatformHistory(w http.ResponseWriter, r *http.Request) {
	page := s.parsePagination(r)
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider != "hero_sms" {
		writeAPI(w, http.StatusOK, map[string]any{"provider": provider, "items": []any{}, "total": 0, "message": "当前仅支持 hero-sms 激活历史"}, "")
		return
	}
	cfg, _ := s.store.SMSConfigRaw(provider)
	apiKey := strings.TrimSpace(fmt.Sprint(cfg["api_key"]))
	if apiKey == "" || apiKey == "<nil>" {
		writeAPI(w, http.StatusBadRequest, nil, "未配置 hero-sms API Key")
		return
	}
	base := strings.TrimSpace(fmt.Sprint(cfg["base_url"]))
	if base == "" {
		base = "https://hero-sms.com/stubs/handler_api.php"
	}
	u, err := url.Parse(base)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, "hero-sms Base URL 无效")
		return
	}
	// Hero-SMS currently exposes activation history through the compatible
	// sms-activate style handler endpoint.  The public REST documentation
	// links to /activations/history, but that route is not available for all
	// API keys (and returns 404/401); handler_api.php?action=getHistory is
	// the endpoint that works with the same key used for getBalance/getNumber.
	legacyHandler := strings.Contains(strings.ToLower(u.Path), "handler_api.php") || strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/stubs")
	if legacyHandler {
		if !strings.Contains(strings.ToLower(u.Path), "handler_api.php") {
			u.Path = strings.TrimRight(u.Path, "/") + "/handler_api.php"
		}
	} else {
		u.Path = strings.TrimRight(u.Path, "/") + "/activations/history"
	}
	q := u.Query()
	q.Set("api_key", apiKey)
	if legacyHandler {
		q.Set("action", "getHistory")
	}
	if paginationRequested(r) {
		if legacyHandler {
			q.Set("limit", strconv.Itoa(page.Offset+page.Limit))
		} else {
			q.Set("limit", strconv.Itoa(page.Limit))
			q.Set("offset", strconv.Itoa(page.Offset))
			q.Set("page", strconv.Itoa(page.Page))
		}
	} else if limit := strings.TrimSpace(r.URL.Query().Get("limit")); limit != "" {
		q.Set("limit", limit)
	}
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "chatgpt-space-merge/1.0")
	// Cloudflare occasionally closes the first keep-alive/HTTP2 connection
	// with EOF.  Use a short-lived HTTP/1.1 transport and retry once so the
	// management page does not fail on an otherwise valid history request.
	historyClient := &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{ForceAttemptHTTP2: false, DisableKeepAlives: true}}
	resp, err := historyClient.Do(req)
	if err != nil {
		if retryResp, retryErr := historyClient.Do(req); retryErr == nil {
			resp, err = retryResp, nil
		}
	}
	if err != nil {
		writeAPI(w, http.StatusBadGateway, nil, "激活历史请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var payload any
	if json.Unmarshal(raw, &payload) != nil {
		writeAPI(w, http.StatusBadGateway, nil, fmt.Sprintf("激活历史响应不是有效 JSON（HTTP %d）", resp.StatusCode))
		return
	}
	// Some Hero-SMS accounts do not expose the documented REST route.  If a
	// custom REST base was configured and it returns 401/404, transparently
	// retry the compatible handler endpoint before reporting an error.
	if !legacyHandler && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound) {
		fallbackURL := *u
		fallbackURL.Path = "/stubs/handler_api.php"
		fq := fallbackURL.Query()
		fq.Set("api_key", apiKey)
		fq.Set("action", "getHistory")
		if paginationRequested(r) {
			fq.Set("limit", strconv.Itoa(page.Offset+page.Limit))
		} else if limit := strings.TrimSpace(r.URL.Query().Get("limit")); limit != "" {
			fq.Set("limit", limit)
		}
		fallbackURL.RawQuery = fq.Encode()
		fallbackReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, fallbackURL.String(), nil)
		fallbackReq.Header.Set("Accept", "application/json")
		fallbackReq.Header.Set("User-Agent", "chatgpt-space-merge/1.0")
		if fallbackResp, fallbackErr := historyClient.Do(fallbackReq); fallbackErr == nil {
			defer fallbackResp.Body.Close()
			fallbackRaw, _ := io.ReadAll(io.LimitReader(fallbackResp.Body, 4<<20))
			var fallbackPayload any
			if json.Unmarshal(fallbackRaw, &fallbackPayload) == nil && fallbackResp.StatusCode >= 200 && fallbackResp.StatusCode < 300 {
				resp.StatusCode = fallbackResp.StatusCode
				raw, payload = fallbackRaw, fallbackPayload
				legacyHandler = true
			}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeAPI(w, http.StatusBadGateway, nil, fmt.Sprintf("激活历史请求失败 HTTP %d: %s", resp.StatusCode, compactSMSJSON(payload)))
		return
	}
	items := payload
	providerTotal := 0
	if obj, ok := payload.(map[string]any); ok {
		for _, key := range []string{"total", "count", "total_count"} {
			if value, exists := obj[key]; exists {
				if parsed, parseErr := strconv.Atoi(fmt.Sprint(value)); parseErr == nil && parsed >= 0 {
					providerTotal = parsed
					break
				}
			}
		}
		for _, key := range []string{"items", "data", "activations", "history", "results"} {
			if value, exists := obj[key]; exists {
				items = value
				break
			}
		}
	}
	total := providerTotal
	if list, ok := items.([]any); ok {
		sort.SliceStable(list, func(i, j int) bool { return smsActivationTimestamp(list[i]) > smsActivationTimestamp(list[j]) })
		if total == 0 {
			total = len(list)
		}
		if paginationRequested(r) && legacyHandler {
			start := page.Offset
			if start > len(list) {
				start = len(list)
			}
			end := start + page.Limit
			if end > len(list) {
				end = len(list)
			}
			items = list[start:end]
		}
	}
	data := map[string]any{"provider": provider, "items": items, "total": total}
	if paginationRequested(r) {
		data["page"], data["page_size"] = page.Page, page.PageSize
	}
	writeAPI(w, http.StatusOK, data, "")
}

func smsActivationTimestamp(value any) int64 {
	item, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	for _, key := range []string{"created_at", "createdAt", "timestamp", "time", "date"} {
		raw := strings.TrimSpace(fmt.Sprint(item[key]))
		if raw == "" || raw == "<nil>" {
			continue
		}
		if numeric, err := strconv.ParseFloat(raw, 64); err == nil {
			if numeric < 1e12 {
				numeric *= 1000
			}
			return int64(numeric)
		}
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return parsed.UnixMilli()
		}
	}
	return 0
}

func compactSMSJSON(value any) string {
	raw, _ := json.Marshal(value)
	text := strings.TrimSpace(string(raw))
	if len(text) > 500 {
		text = text[:500] + "..."
	}
	return text
}

func firstSMSConfig(cfg map[string]any, key, fallback string) string {
	if v := strings.TrimSpace(fmt.Sprint(cfg[key])); v != "" && v != "<nil>" {
		return v
	}
	return fallback
}
