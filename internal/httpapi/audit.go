package httpapi

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

var (
	sensitiveQueryValuePattern = regexp.MustCompile(`(?i)(access_token|refresh_token|id_token|code_verifier|authorization|cookie|state|code)=([^&\s]+)`)
	bearerValuePattern         = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	jwtValuePattern            = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]{8,})?`)
	oauthTokenValuePattern     = regexp.MustCompile(`\b(?:rt_|ac_)[A-Za-z0-9._~-]{10,}`)
	otpValuePattern            = regexp.MustCompile(`(?i)(otp|verification[ _-]?code|验证码)(\s*[:=：]?\s*)\d{6}\b`)
	exactOTPValuePattern       = regexp.MustCompile(`^\d{6}$`)
	phoneValuePattern          = regexp.MustCompile(`\+\d[\d -]{7,16}\d`)
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
	if event.CycleID == "" && event.AccountID != "" {
		if cycle, ok := s.accountCycles.Load(event.AccountID); ok {
			event.CycleID, _ = cycle.(string)
		}
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	if event.Level == "" {
		event.Level = "info"
	}
	event.Request = redactMap(event.Request)
	event.Response = redactMap(event.Response)
	event.Details = redactMap(event.Details)
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
		CycleID: trace.CycleID,
		RunID:   trace.RunID, TaskID: trace.TaskID, AccountID: accountID,
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
		if metadata, ok := redactOAuthMetadata(lower, value); ok {
			out[key] = metadata
			continue
		}
		if strings.Contains(lower, "token") || strings.Contains(lower, "cookie") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "authorization") || strings.Contains(lower, "management-key") || strings.Contains(lower, "management_key") {
			out[key] = "***"
			continue
		}
		if lower == "code" || strings.Contains(lower, "otp") || strings.Contains(lower, "verification_code") || strings.Contains(lower, "verification-code") {
			if text, ok := value.(string); ok && exactOTPValuePattern.MatchString(strings.TrimSpace(text)) {
				out[key] = "***"
				continue
			}
		}
		out[key] = redactValue(value)
	}
	return out
}

// Preserve typed diagnostics, never Cookie values or arbitrary credential fields.
func redactOAuthMetadata(key string, value any) (any, bool) {
	switch key {
	case "auth_session_cookie_present", "access_token_present", "refresh_token_present", "id_token_present":
		present, ok := value.(bool)
		return present, ok
	case "cookie_jar":
		items, ok := value.([]any)
		if !ok {
			return nil, false
		}
		rows := make([]any, 0, len(items))
		for _, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			row := map[string]any{}
			for _, field := range []string{"name", "domain", "path"} {
				if text, ok := entry[field].(string); ok {
					row[field] = redactValue(text)
				}
			}
			if secure, ok := entry["secure"].(bool); ok {
				row["secure"] = secure
			}
			rows = append(rows, row)
		}
		return rows, true
	case "set_cookie_names":
		items, ok := value.([]any)
		if !ok {
			return nil, false
		}
		names := make([]any, 0, len(items))
		for _, item := range items {
			if name, ok := item.(string); ok {
				names = append(names, redactValue(name))
			}
		}
		return names, true
	}
	return nil, false
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
		item = redactSensitiveText(item)
		if len(item) > 16000 {
			return fmt.Sprintf("%s… [已截断]", item[:16000])
		}
		return item
	}
	return value
}

func redactSensitiveText(value string) string {
	value = sensitiveQueryValuePattern.ReplaceAllString(value, "$1=***")
	value = bearerValuePattern.ReplaceAllString(value, "Bearer ***")
	value = jwtValuePattern.ReplaceAllString(value, "***")
	value = oauthTokenValuePattern.ReplaceAllString(value, "***")
	value = otpValuePattern.ReplaceAllString(value, "$1$2***")
	value = phoneValuePattern.ReplaceAllString(value, "+***")
	return value
}
