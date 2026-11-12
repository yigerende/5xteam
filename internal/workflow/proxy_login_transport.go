package workflow

import (
	"context"
	"io"
	"net/http"
	"time"
)

// A plain TLS request checks the login route. It never submits credentials or
// solves a challenge. HTTP authentication/challenge responses prove transport
// reachability; proxy authentication failures and gateway failures do not.
func TestProxyLoginTransport(ctx context.Context, proxyURL string) bool {
	client, err := proxyHTTPClient(proxyURL, 15*time.Second)
	if err != nil {
		return false
	}
	defer closeProxyIdleConnections(client)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	for _, target := range []string{"https://auth.openai.com/", "https://chatgpt.com/"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return false
		}
		response, err := client.Do(req)
		if err != nil {
			return false
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 500 || response.StatusCode == http.StatusProxyAuthRequired {
			return false
		}
	}
	return true
}
