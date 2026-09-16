package httpapi

import (
	"context"
	"fmt"
	"log/slog"
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
	defer close(s.auditDone)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]model.AutoRotationEvent, 0, 64)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := s.store.AddAutoRotationEvents(batch); err != nil {
			s.recordAuditWriteError(err)
			return err
		}
		batch = batch[:0]
		return nil
	}
	// Drain only the captured queue length; ongoing jobs must not delay export
	// indefinitely. Failed batches stay in memory for the next flush attempt.
	drain := func() error {
		if err := flush(); err != nil {
			return err
		}
		for remaining := len(s.auditQueue); remaining > 0; remaining-- {
			batch = append(batch, <-s.auditQueue)
			if len(batch) >= 64 {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		return flush()
	}
	for {
		queue := s.auditQueue
		if len(batch) >= 64 {
			queue = nil // Bound memory while SQLite is unavailable.
		}
		select {
		case event := <-queue:
			batch = append(batch, event)
			if len(batch) >= 64 {
				_ = flush()
			}
		case reply := <-s.auditFlush:
			reply <- drain()
		case <-s.auditStop:
			if err := drain(); err != nil {
				s.auditErrorMu.Lock()
				s.auditShutdownErr = err
				s.auditErrorMu.Unlock()
				slog.Error("audit shutdown left unwritten events", "pending", len(batch)+len(s.auditQueue))
			}
			return
		case <-ticker.C:
			_ = flush()
		}
	}
}

func (s *Server) recordAuditWriteError(err error) {
	s.auditWriteErrors.Add(1)
	s.auditErrorMu.Lock()
	defer s.auditErrorMu.Unlock()
	message := redactSensitiveText(err.Error())
	if s.auditLastError != message {
		slog.Error("audit persistence failed; pending batch will be retried", "error", message)
	}
	s.auditLastError = message
}

func (s *Server) flushAuditEvents(ctx context.Context) error {
	if s.auditFlush == nil {
		return fmt.Errorf("audit writer is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reply := make(chan error, 1)
	select {
	case s.auditFlush <- reply:
	case <-s.auditDone:
		s.auditErrorMu.Lock()
		defer s.auditErrorMu.Unlock()
		return s.auditShutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Ordinary logging stays nonblocking. Export reports any queue overflow so
// a partial timeline cannot be mistaken for an uneventful execution.
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
	if event.Details == nil {
		event.Details = map[string]any{}
	}
	event.Details["diagnostics_version"] = 2
	event.Message = redactSensitiveText(event.Message)
	// Dead-account handling is an exceptional terminal path. Persist its
	// markers immediately so an operator (or a subsequent recovery request)
	// can observe the decision before the removal call returns. All ordinary
	// business events remain fully asynchronous below.
	if strings.HasPrefix(event.Type, "dead_") {
		if err := s.store.AddAutoRotationEvent(event); err == nil {
			return
		} else {
			s.recordAuditWriteError(err)
		}
	}
	select {
	case s.auditQueue <- event:
	default:
		if s.auditDropped.Add(1) == 1 {
			slog.Error("audit queue full; export will report dropped events")
		}
	}
}

func (s *Server) auditAccountEvent(ctx context.Context, accountID, operation, stage, source, provider, message string, details map[string]any) {
	s.auditAccountEventWithIO(ctx, accountID, operation, stage, source, provider, message, details, nil, nil)
}

func (s *Server) auditAccountEventWithIO(ctx context.Context, accountID, operation, stage, source, provider, message string, details, request, response map[string]any) {
	trace, _ := ctx.Value(autoRotationTraceContextKey{}).(autoRotationTraceContext)
	if trace.Attempt > 0 {
		copyDetails := make(map[string]any, len(details)+2)
		for key, value := range details {
			copyDetails[key] = value
		}
		copyDetails["outer_attempt"], copyDetails["outer_max_attempts"] = trace.Attempt, trace.MaxAttempts
		details = copyDetails
	}
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
	case "auth_session_cookie_present", "login_session_cookie_present", "access_token_present", "refresh_token_present", "id_token_present":
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
