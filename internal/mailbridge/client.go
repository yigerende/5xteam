package mailbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client talks to the proven mail and OpenAI login implementation in
// gpt-account-manager. Keeping it behind this small adapter lets the main app
// stay a compact Go binary while the protocol-heavy mailbox code remains in
// one place.
type Client struct {
	baseURL string
	http    *http.Client
}

func New() *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MAIL_MANAGER_URL")), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8765"
	}
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 3 * time.Minute}}
}

func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) (map[string]any, error) {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Workspace-Id", "public")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("邮件服务不可用（%s）: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("邮件服务响应无效: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 || payload["success"] == false {
		message, _ := payload["error"].(string)
		if strings.TrimSpace(message) == "" {
			message = fmt.Sprintf("邮件服务返回 HTTP %d", resp.StatusCode)
		}
		return nil, errors.New(message)
	}
	return payload, nil
}
