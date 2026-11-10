// Package refundmerchant implements the merchant's submit/progress API.
package refundmerchant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const URL = "https://refunds.llmrefund.com"
const DefaultSubmitURL = URL + "/v1/refunds"

type Account struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	TOTPSecret string `json:"totp_secret"`
}
type Submission struct {
	Authorized bool      `json:"authorized"`
	Accounts   []Account `json:"accounts"`
}
type Job struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Status        string `json:"status"`
	AccountStatus string `json:"account_status"`
}

// Only the merchant's explicit final status is success. HTTP 202, a batch ID,
// and an empty/unknown status are never proof that the refund was processed.
func (j Job) Succeeded() bool { return j.Status == "succeeded" && j.AccountStatus != "banned" }
func (j Job) TerminalFailure() bool {
	return j.Status == "failed" || j.Status == "cancelled" || j.Status == "banned" || j.AccountStatus == "banned"
}
func (j Job) Label() string {
	if j.AccountStatus == "banned" {
		return "商户报告账号已封号"
	}
	switch j.Status {
	case "queued":
		return "商户排队中"
	case "processing":
		return "商户处理中"
	case "verifying":
		return "商户核验中"
	case "attention":
		return "商户待跟进"
	case "succeeded":
		return "商户处理成功"
	case "failed":
		return "商户处理失败"
	case "cancelled":
		return "商户申请已取消"
	case "banned":
		return "商户报告账号已封号"
	default:
		return "商户返回未知进度，继续查询"
	}
}

type Client struct {
	Provider string
	// SubmitURL is the complete endpoint. BaseURL is retained for existing
	// callers using the original API at a different host.
	SubmitURL string
	BaseURL   string
	HTTP      *http.Client
}

func (c Client) request(ctx context.Context, method, path, key, idempotency string, payload []byte) ([]byte, error) {
	endpoint := c.SubmitURL
	if endpoint == "" && c.BaseURL != "" {
		endpoint = strings.TrimRight(c.BaseURL, "/") + "/v1/refunds"
	}
	endpoint, err := NormalizeProviderURL(c.Provider, endpoint)
	if err != nil {
		return nil, err
	}
	address := endpoint + path
	if strings.HasPrefix(path, "/") {
		address = strings.TrimRight(endpoint, "/") + path
	}
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("商户请求地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idempotency)
	}
	client := http.Client{Timeout: 45 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	// Credentials must never be forwarded to a redirected host.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("商户接口连接失败或超时，保留原申请幂等键")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("商户接口 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("读取商户响应失败")
	}
	return raw, nil
}

type Receipt struct {
	JobID   string
	BatchID string
}

func (c Client) Submit(ctx context.Context, key, idempotency string, payload []byte, email string) (Receipt, error) {
	raw, err := c.request(ctx, http.MethodPost, "", key, idempotency, payload)
	if err != nil {
		return Receipt{}, err
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return Receipt{}, errors.New("商户已响应，但申请编号无法解析；续跑仍使用原幂等键")
	}
	if c.Provider == ProviderStarBridge {
		var response struct {
			IDs []string `json:"ids"`
		}
		if json.Unmarshal(raw, &response) != nil || len(response.IDs) != 1 || !validID(response.IDs[0]) {
			return Receipt{}, errors.New("星桥未返回唯一申请编号；续跑仍使用原幂等键")
		}
		return Receipt{JobID: response.IDs[0]}, nil
	}
	// The published OpenAPI leaves response schemas unspecified. Accept single
	// account envelopes, never treat a batch_id as a queryable account job ID.
	id := submissionID(value, email)
	batch := ""
	if obj, ok := value.(map[string]any); ok {
		batch, _ = obj["batch_id"].(string)
	}
	if !validID(batch) {
		batch = ""
	}
	if id == "" {
		if batch != "" {
			return Receipt{BatchID: batch}, nil
		}
		return Receipt{}, errors.New("商户已受理但未返回可识别的申请编号；未标记完成，续跑使用原幂等键")
	}
	return Receipt{JobID: id, BatchID: batch}, nil
}
func submissionID(v any, email string) string {
	switch x := v.(type) {
	case []any:
		if len(x) == 1 {
			return submissionID(x[0], email)
		}
	case map[string]any:
		if actual, ok := x["email"].(string); ok && !strings.EqualFold(actual, email) {
			return ""
		}
		for _, name := range []string{"job_ids", "ids"} {
			if ids, ok := x[name].([]any); ok && len(ids) == 1 {
				if id, ok := ids[0].(string); ok && validID(id) {
					return id
				}
			}
		}
		for _, name := range []string{"jobs", "items", "refunds", "accounts", "data"} {
			if child, exists := x[name]; exists {
				return submissionID(child, email)
			}
		}
		if id, ok := x["job_id"].(string); ok && validID(id) {
			return id
		}
		if id, ok := x["id"].(string); ok && validID(id) {
			_, batch := x["batch_id"]
			_, identity := x["email"]
			if !batch || identity {
				return id
			}
		}
	}
	return ""
}

// Some versions return a batch receipt. Resolve its one submitted account from
// the documented batch filter, then use GET {SubmitURL}/{id} for all checks.
func (c Client) ResolveBatch(ctx context.Context, key, batch, email string) (string, error) {
	if c.Provider == ProviderStarBridge {
		return "", errors.New("星桥申请须按订单编号查询，不支持批次查询")
	}
	if !validID(batch) {
		return "", errors.New("商户批次编号无效")
	}
	raw, err := c.request(ctx, http.MethodGet, "?batch_id="+url.QueryEscape(batch)+"&page_size=100&page=1", key, "", nil)
	if err != nil {
		return "", err
	}
	var response struct {
		Items []struct {
			Job
			BatchID string `json:"batch_id"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return "", errors.New("商户批次进度格式无效")
	}
	id := ""
	for _, job := range response.Items {
		if strings.EqualFold(job.Email, email) && validID(job.ID) && (job.BatchID == "" || job.BatchID == batch) {
			if id != "" {
				return "", errors.New("商户批次返回多个同账号申请，未猜测处理结果")
			}
			id = job.ID
		}
	}
	if id == "" {
		return "", errors.New("商户批次尚未返回该账号的申请编号，稍后继续查询")
	}
	return id, nil
}
func validID(id string) bool {
	return id != "" && len(id) <= 200 && !strings.ContainsAny(id, "/?#\\\r\n\t ")
}
func (c Client) Progress(ctx context.Context, key, id, email string) (Job, error) {
	var job Job
	if !validID(id) {
		return job, errors.New("商户申请编号无效")
	}
	raw, err := c.request(ctx, http.MethodGet, "/"+url.PathEscape(id), key, "", nil)
	if err != nil {
		return job, err
	}
	var response struct {
		Job
		Refund *Job `json:"refund"`
		Order  *Job `json:"order"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return Job{}, errors.New("商户进度格式无效")
	}
	if c.Provider == ProviderStarBridge {
		if response.Order == nil {
			return Job{}, errors.New("星桥进度缺少 order，未标记完成")
		}
		job = *response.Order
	} else {
		job = response.Job
		if response.Refund != nil && job.ID == "" {
			job = *response.Refund
		}
	}
	if job.ID != id || !strings.EqualFold(job.Email, email) || job.Status == "" {
		return Job{}, errors.New("商户进度缺少状态或申请编号、账号不匹配，未标记完成")
	}
	return job, nil
}
