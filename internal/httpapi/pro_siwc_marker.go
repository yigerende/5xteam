package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/sub2"
)

func (s *Server) startSIWCMarkerWorker(ctx context.Context) {
	s.proAutoWG.Add(1)
	go func() {
		defer s.proAutoWG.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		// Dedicated HTTP client: its admin login mutex cannot delay quota queries.
		client := sub2.New()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tasks, err := s.store.ClaimSIWCMarkers(time.Now())
				if err != nil {
					slog.Warn("读取 SIWC 标记任务失败", "error", err)
					continue
				}
				for _, t := range tasks {
					if ctx.Err() != nil {
						return
					}
					s.syncSIWCMarker(ctx, client, t)
				}
			}
		}
	}()
}

func (s *Server) syncSIWCMarker(parent context.Context, client *sub2.Client, t store.SIWCMarkerTask) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	state, message, next := "synced", "", time.Time{}
	settings, password, _, err := s.proAccountPushSettings(t.Profile)
	if err == nil && (settings.Provider != "sub2" || settings.Sub2.AuthMode != "siwc" || t.Profile.Sub2AccountID <= 0) {
		state = "skipped"
	}
	if err == nil && state != "skipped" {
		ref := t.Profile.ProPushDownstream
		if ref == nil || !ref.Matches(model.ProDownstream(settings)) {
			err = errors.New("原 SIWC 推送目标不匹配，未更新其他站点账号")
		}
	}
	if err == nil && state != "skipped" {
		marker := t.Profile.SIWCWorkflowMarker(t.SourceID, t.Revision, t.ChangedAt, settings.Disposition())
		// Always deliver the newest revision. An older request can already be
		// in flight when the state changes back to a previously synced value.
		err = client.SyncSpaceWorkflow(ctx, settings.Sub2, password, t.Profile.Sub2AccountID, marker)
	}
	if err != nil {
		state = "retry"
		// Don't copy upstream response bodies, tokens or proxy URLs into the list.
		message = "SIWC 标记同步失败，将自动重试"
		var httpErr *sub2.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.Status {
			case 401, 403:
				message = "SIWC 管理认证失败，检查推送连接后重试"
			case 404:
				message = "SIWC 标记接口或原账号不存在，等待更新/核对后重试"
			case 409:
				message = "SIWC 标记账号或版本冲突，需核对原绑定"
			}
		}
		delays := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute}
		next = time.Now().Add(delays[min(t.Attempts, len(delays)-1)])
	}
	if err := s.store.FinishSIWCMarker(t, state, message, next); err != nil {
		slog.Warn("保存 SIWC 标记同步状态失败", "error", err)
	}
}

func (s *Server) retrySIWCMarker(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RetrySIWCMarker(r.PathValue("email")); err != nil {
		writeAPI(w, 500, nil, "保存标记重试失败")
		return
	}
	writeAPI(w, 200, map[string]bool{"queued": true}, "")
}
