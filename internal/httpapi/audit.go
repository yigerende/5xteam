package httpapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

// auditWriter is deliberately separate from the request/worker goroutines.
// Business code only performs a bounded channel send; SQLite writes happen in
// batches here and therefore cannot add network latency to Team rotation.
func (s *Server) auditWriter() {
	defer s.auditWG.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]model.AutoRotationEvent, 0, 64)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = s.store.AddAutoRotationEvents(batch)
		batch = batch[:0]
	}
	for {
		select {
		case event := <-s.auditQueue:
			batch = append(batch, event)
			if len(batch) >= 64 {
				flush()
			}
		case <-s.auditStop:
			// Drain the queue once so shutdown does not lose events already
			// accepted by enqueueAuditEvent, then commit the final batch.
			for {
				select {
				case event := <-s.auditQueue:
					batch = append(batch, event)
				default:
					flush()
					return
				}
			}
		case <-ticker.C:
			flush()
		}
	}
}

// enqueueAuditEvent never waits for the database. If the diagnostic queue is
// temporarily full, preserve high-value events by replacing their payload
// with a compact summary and retrying once; normal business flow still wins.
func (s *Server) enqueueAuditEvent(event model.AutoRotationEvent) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	if event.Level == "" {
		event.Level = "info"
	}
	event.Request = redactMap(event.Request)
	event.Response = redactMap(event.Response)
	// Dead-account handling is an exceptional terminal path. Persist its
	// markers immediately so an operator (or a subsequent recovery request)
	// can observe the decision before the removal call returns. All ordinary
	// business events remain fully asynchronous below.
	if strings.HasPrefix(event.Type, "dead_") {
		_ = s.store.AddAutoRotationEvent(event)
		return
	}
	select {
	case s.auditQueue <- event:
	default:
		if event.Level == "error" || event.Type == "retry" || event.Type == "dead_detected" || event.Type == "final" {
			event.Request = nil
			event.Response = nil
			select {
			case s.auditQueue <- event:
			default:
			}
		}
	}
}

func (s *Server) auditAccountEvent(ctx context.Context, accountID, operation, stage, source, provider, message string, details map[string]any) {
	s.auditAccountEventWithIO(ctx, accountID, operation, stage, source, provider, message, details, nil, nil)
}

func (s *Server) auditAccountEventWithIO(ctx context.Context, accountID, operation, stage, source, provider, message string, details, request, response map[string]any) {
	trace, _ := ctx.Value(autoRotationTraceContextKey{}).(autoRotationTraceContext)
	s.enqueueAuditEvent(model.AutoRotationEvent{
		RunID: trace.RunID, TaskID: trace.TaskID, AccountID: accountID,
		Operation: operation, Stage: stage, Source: source, Provider: provider,
		Type: "audit", Message: message, Details: details, Request: request, Response: response,
	})
}

func providerForSettings(settings model.Sub2Settings) string {
	if strings.EqualFold(settings.Provider, "cpa") {
		return "cpa"
	}
	return "sub2"
}

func redactMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		lower := strings.ToLower(strings.TrimSpace(key))
		if strings.Contains(lower, "token") || strings.Contains(lower, "cookie") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "authorization") || strings.Contains(lower, "management-key") || strings.Contains(lower, "management_key") {
			out[key] = "***"
			continue
		}
		out[key] = redactValue(value)
	}
	return out
}

func redactValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		return redactMap(item)
	case []any:
		result := make([]any, len(item))
		for i, child := range item {
			result[i] = redactValue(child)
		}
		return result
	case string:
		if len(item) > 16000 {
			return fmt.Sprintf("%s… [已截断]", item[:16000])
		}
	}
	return value
}
