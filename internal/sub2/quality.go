package sub2

import (
	"bufio"
	"chatgpt-space-merge/internal/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type QualityProbeResult struct {
	AccountID  int64  `json:"account_id"`
	Complete   bool   `json:"complete"`
	Text       string `json:"text"`
	Model      string `json:"model"`
	HTTPStatus int    `json:"http_status"`
	DurationMS int64  `json:"duration_ms"`
	RequestID  string `json:"request_id"`
	Error      string `json:"error"`
}

type ModelAuditAccount struct {
	AccountID int64     `json:"account_id"`
	Since     time.Time `json:"since"`
}
type ModelAuditLog struct {
	ID             int64     `json:"id"`
	AccountID      int64     `json:"account_id"`
	CreatedAt      time.Time `json:"created_at"`
	RequestedModel string    `json:"requested_model"`
	SentModel      string    `json:"sent_model"`
	ResponseModel  string    `json:"response_model"`
	Mismatch       *bool     `json:"mismatch"`
}
type ModelAuditResult struct {
	AccountID int64           `json:"account_id"`
	Logs      []ModelAuditLog `json:"logs"`
}

func (c *Client) ModelAuditCapabilities(ctx context.Context, s model.Sub2Settings, password string) error {
	data, err := c.doJSON(ctx, s, password, http.MethodGet, "/api/v1/admin/usage/model-audit-capabilities", nil, nil)
	if err != nil {
		return fmt.Errorf("Sub2 未提供轻量模型日志接口，请部署配套 Qerkai 版本：%w", err)
	}
	var v struct {
		Version        int `json:"version"`
		MaxAccounts    int `json:"max_accounts"`
		LogsPerAccount int `json:"logs_per_account"`
	}
	if json.Unmarshal(data, &v) != nil || v.Version < 1 || v.MaxAccounts < 10 || v.LogsPerAccount != 3 {
		return errors.New("Sub2 模型日志接口版本不兼容")
	}
	return nil
}
func (c *Client) ModelAudit(ctx context.Context, s model.Sub2Settings, password, modelName string, accounts []ModelAuditAccount) ([]ModelAuditResult, error) {
	data, err := c.doJSON(ctx, s, password, http.MethodPost, "/api/v1/admin/usage/model-audit", map[string]any{"model": modelName, "accounts": accounts}, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Accounts []ModelAuditResult `json:"accounts"`
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	wanted := map[int64]bool{}
	for _, a := range accounts {
		wanted[a.AccountID] = true
	}
	if len(out.Accounts) != len(accounts) {
		return nil, errors.New("Sub2 模型日志返回账号数量不匹配")
	}
	for _, a := range out.Accounts {
		if !wanted[a.AccountID] || len(a.Logs) > 3 {
			return nil, errors.New("Sub2 模型日志账号或条数不匹配")
		}
		delete(wanted, a.AccountID)
		for _, l := range a.Logs {
			if l.AccountID != a.AccountID || l.ID < 1 || l.CreatedAt.IsZero() {
				return nil, errors.New("Sub2 模型日志记录无效")
			}
		}
	}
	return out.Accounts, nil
}

func (c *Client) ProbeQuality(ctx context.Context, s model.Sub2Settings, password string, id int64, q model.QualitySettings) (QualityProbeResult, error) {
	out := QualityProbeResult{AccountID: id}
	if id < 1 {
		return out, errors.New("Sub2 账号 ID 无效")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(q.TimeoutSeconds)*time.Second)
	defer cancel()
	prompt := q.Prompt
	if q.Mode != "time" && q.MatchMode == "answer" {
		prompt += "\n最后一行严格输出 FINAL_ANSWER=你的最终答案，不要在这一行附加其他内容。"
	}
	body := map[string]any{"model_id": q.Model, "prompt": prompt, "reasoning_effort": q.ReasoningEffort}
	path := "/api/v1/admin/accounts/" + strconv.FormatInt(id, 10) + "/test"
	headers := map[string]string{"Accept": "text/event-stream"}
	response, err := c.request(ctx, s, password, http.MethodPost, path, body, headers)
	if err != nil {
		return out, err
	}
	if response.StatusCode == http.StatusUnauthorized {
		response.Body.Close()
		c.invalidate()
		response, err = c.request(ctx, s, password, http.MethodPost, path, body, headers)
		if err != nil {
			return out, err
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return out, fmt.Errorf("Sub2 单账号测试 HTTP %d: %s", response.StatusCode, compact(data))
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return out, errors.New("Sub2 单账号测试未返回 SSE 流")
	}
	out.RequestID = response.Header.Get("X-Request-ID")
	return readQualityTestStream(response.Body, out, q.ReasoningEffort)
}

var qualityUpstreamStatus = regexp.MustCompile(`^API returned ([1-5][0-9]{2}):`)

func readQualityTestStream(body io.Reader, out QualityProbeResult, effort string) (QualityProbeResult, error) {
	const maxBytes = 2 << 20
	scanner := bufio.NewScanner(io.LimitReader(body, maxBytes+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data, answer strings.Builder
	var started time.Time
	var supported bool
	readBytes := 0
	consume := func() (bool, error) {
		if data.Len() == 0 {
			return false, nil
		}
		raw := data.String()
		data.Reset()
		if strings.TrimSpace(raw) == "[DONE]" {
			return true, errors.New("Sub2 测试流缺少完成事件")
		}
		var event struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Model   string `json:"model"`
			Success bool   `json:"success"`
			Error   string `json:"error"`
			Data    struct {
				CustomPrompt    bool   `json:"custom_prompt"`
				ReasoningEffort string `json:"reasoning_effort"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return true, errors.New("Sub2 测试流事件格式无效")
		}
		switch event.Type {
		case "test_start":
			if !started.IsZero() {
				return true, errors.New("Sub2 测试流重复开始")
			}
			started = time.Now()
			supported = event.Data.CustomPrompt && (effort == "" || event.Data.ReasoningEffort == effort)
			out.Model = event.Model
		case "content":
			answer.WriteString(event.Text)
		case "error":
			out.Error = event.Error
			if out.Error == "" {
				out.Error = "Sub2 单账号测试失败"
			}
			if match := qualityUpstreamStatus.FindStringSubmatch(out.Error); len(match) == 2 {
				out.HTTPStatus, _ = strconv.Atoi(match[1])
			}
			return true, nil
		case "test_complete":
			if !event.Success {
				return true, errors.New("Sub2 单账号测试未成功完成")
			}
			if !supported || started.IsZero() {
				return true, errors.New("Sub2 OAuth 单账号测试未确认使用自定义题目/推理强度，请更新配套 Sub2")
			}
			if strings.TrimSpace(answer.String()) == "" {
				return true, errors.New("Sub2 单账号测试返回空回答")
			}
			out.Complete, out.HTTPStatus = true, http.StatusOK
			out.DurationMS = time.Since(started).Milliseconds()
			return true, nil
		}
		return false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		readBytes += len(line) + 1
		if readBytes > maxBytes {
			return out, errors.New("Sub2 测试流超过大小限制")
		}
		if line == "" {
			done, err := consume()
			if done || err != nil {
				out.Text = answer.String()
				return out, err
			}
		} else if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("Sub2 测试流读取失败: %w", err)
	}
	done, err := consume()
	out.Text = answer.String()
	if done || err != nil {
		return out, err
	}
	return out, errors.New("Sub2 测试流提前结束，缺少完成事件")
}
func (c *Client) AccountGroups(ctx context.Context, s model.Sub2Settings, password string, id int64) ([]int64, error) {
	data, err := c.doJSON(ctx, s, password, http.MethodGet, "/api/v1/admin/accounts/"+strconv.FormatInt(id, 10), nil, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		ID       int64   `json:"id"`
		GroupIDs []int64 `json:"group_ids"`
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out.ID != id {
		return nil, errors.New("Sub2 账号响应 ID 不匹配，拒绝覆盖分组")
	}
	// Sub2 omits group_ids for an empty group set.
	return uniquePositiveIDs(out.GroupIDs), nil
}
func (c *Client) SetAccountGroups(ctx context.Context, s model.Sub2Settings, password string, id int64, groups []int64) error {
	groups = uniquePositiveIDs(groups)
	if groups == nil {
		groups = []int64{}
	}
	_, err := c.doJSON(ctx, s, password, http.MethodPut, "/api/v1/admin/accounts/"+strconv.FormatInt(id, 10), map[string]any{"group_ids": groups}, nil)
	// An interrupted write may already have committed. Verify before retrying.
	actual, verifyErr := c.AccountGroups(ctx, s, password, id)
	if verifyErr == nil && equalGroupIDs(groups, actual) {
		return nil
	}
	if err != nil {
		return err
	}
	if verifyErr != nil {
		return verifyErr
	}
	return errors.New("Sub2 分组写入后校验不一致")
}
func equalGroupIDs(a, b []int64) bool {
	a = uniquePositiveIDs(a)
	b = uniquePositiveIDs(b)
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
