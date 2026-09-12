package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type mailExportProgressKey struct{}

func reportMailExportProgress(ctx context.Context, stage string, processed, total int) {
	if report, ok := ctx.Value(mailExportProgressKey{}).(func(string, int, int)); ok {
		report(stage, processed, total)
	}
}

// Metadata is newline-delimited JSON. After the "file" metadata line, the
// remaining response is the original file bytes (including binary ZIP data).
func (s *Server) exportMailAccountCredentialsProgress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-mail-export")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	send := func(event any) {
		if ctx.Err() != nil {
			return
		}
		if err := json.NewEncoder(w).Encode(event); err != nil {
			cancel()
			return
		}
		_ = http.NewResponseController(w).Flush()
	}
	var last time.Time
	report := func(stage string, processed, total int) {
		// Avoid a flush per account on large exports while preserving real counts.
		if stage == "processing" && processed > 0 && processed < total && time.Since(last) < 100*time.Millisecond {
			return
		}
		last = time.Now()
		send(map[string]any{"type": "progress", "stage": stage, "processed": processed, "total": total})
	}
	stream := &mailExportStreamWriter{target: w, header: make(http.Header), send: send}
	s.exportMailAccountCredentialsBatch(stream, r.WithContext(context.WithValue(ctx, mailExportProgressKey{}, report)))
	if ctx.Err() != nil {
		return
	}
	if stream.status >= 400 {
		var result response
		_ = json.Unmarshal(stream.failure.Bytes(), &result)
		if result.Error == "" {
			result.Error = "导出失败，请重试"
		}
		send(map[string]any{"type": "error", "error": result.Error})
	}
}

type mailExportStreamWriter struct {
	target  http.ResponseWriter
	header  http.Header
	status  int
	started bool
	failure bytes.Buffer
	send    func(any)
}

func (w *mailExportStreamWriter) Header() http.Header { return w.header }

func (w *mailExportStreamWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *mailExportStreamWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 {
		return w.failure.Write(data)
	}
	if !w.started {
		w.started = true
		headers := make(map[string]string)
		for key := range w.header {
			headers[key] = w.header.Get(key)
		}
		w.send(map[string]any{"type": "file", "headers": headers, "size": len(data)})
	}
	return w.target.Write(data)
}
