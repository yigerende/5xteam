package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
)

func (s *Server) beginProManualStage(email, stage string) func(error) {
	id := randomRegistrationID()
	_ = s.store.StartProManualStage(email, stage, id)
	return func(err error) {
		status, message := "completed", ""
		if err != nil {
			status, message = "failed", err.Error()
		}
		if errors.Is(err, errProTransferUncertain) {
			status = "unknown"
		}
		_ = s.store.FinishProManualStage(email, stage, id, status, message)
	}
}

// These handlers return one JSON envelope (no streaming). Observe only its
// error field; token/card/session response data is never retained here.
type proStageResponseWriter struct {
	http.ResponseWriter
	status  int
	message string
}

func (w *proStageResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *proStageResponseWriter) Write(b []byte) (int, error) {
	if w.status >= 400 {
		var envelope struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &envelope) == nil {
			w.message = envelope.Error
		}
	}
	return w.ResponseWriter.Write(b)
}
func (s *Server) trackProStageResponse(w http.ResponseWriter, email, stage string) (http.ResponseWriter, func()) {
	finish := s.beginProManualStage(email, stage)
	tracked := &proStageResponseWriter{ResponseWriter: w}
	return tracked, func() {
		if tracked.status >= 400 {
			if tracked.message == "" {
				tracked.message = http.StatusText(tracked.status)
			}
			finish(errors.New(tracked.message))
		} else {
			finish(nil)
		}
	}
}

func proPaymentStage(order gptpay.Order) model.ProStageProgress {
	status := "running"
	switch order.Status {
	case "success":
		status = "completed"
	case "failed":
		status = "failed"
	case "submission_unknown":
		status = "unknown"
	}
	return model.ProStageProgress{ID: order.ID, Status: status, Error: order.Error, StartedAt: order.CreatedAt, UpdatedAt: time.Now()}
}

func (s *Server) updateProPaymentStage(order gptpay.Order) {
	progress := proPaymentStage(order)
	_, _ = s.store.UpdateProAccount(order.Email, func(p *model.MailAccountProfile) {
		if p.ProAuto.Status == "running" {
			return
		}
		if p.ProManualStages == nil {
			p.ProManualStages = map[string]model.ProStageProgress{}
		}
		previous := p.ProManualStages["recharge"]
		if previous.ID != order.ID && previous.StartedAt.After(order.CreatedAt) {
			return
		}
		if previous.ID == order.ID && (previous.Status == "completed" || previous.Status == "failed") && previous.Status != progress.Status {
			return
		}
		p.ProManualStages["recharge"] = progress
	})
}
