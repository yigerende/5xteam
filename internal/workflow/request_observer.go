package workflow

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// RequestObservation is opt-in diagnostics. It never contains authentication
// headers; callers must redact structured payloads before persisting them.
type RequestObservation struct {
	Phase, Method, URL, Transport string
	Headers                       map[string]any
	Payload                       any
	StatusCode                    int
	Body                          []byte
	Duration                      time.Duration
	Err                           error
}

type requestObserverKey struct{}

func WithRequestObserver(ctx context.Context, observer func(RequestObservation)) context.Context {
	return context.WithValue(ctx, requestObserverKey{}, observer)
}

func diagnosticHeaders(headers http.Header) map[string]any {
	out := make(map[string]any, len(headers))
	for key, values := range headers {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") || strings.Contains(lower, "token") {
			continue
		}
		out[key] = strings.Join(values, ", ")
	}
	return out
}
