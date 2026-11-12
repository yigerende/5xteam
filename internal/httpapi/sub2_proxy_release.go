package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

func (s *Server) registerSub2ProxyRelease(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sub2-proxy-bindings", func(w http.ResponseWriter, r *http.Request) {
		ids := uniqueNonEmptyStrings(strings.Split(r.URL.Query().Get("ids"), ","))
		if len(ids) > 500 {
			writeAPI(w, 400, nil, "一次最多读取 500 个账号")
			return
		}
		rows, err := s.store.Sub2ProxyBindings(ids)
		if err != nil {
			writeAPI(w, 500, nil, "读取代理绑定状态失败")
			return
		}
		for id, b := range rows {
			b.Endpoint, b.TargetID, b.Scope = "", "", ""
			rows[id] = b
		}
		writeAPI(w, 200, map[string]any{"items": rows, "server_time": time.Now()}, "")
	})
	mux.HandleFunc("POST /api/sub2-proxy-bindings/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.RetrySub2ProxyRelease(r.PathValue("id"), time.Now()); err != nil {
			writeAPI(w, 409, nil, err.Error())
			return
		}
		writeAPI(w, 200, map[string]bool{"queued": true}, "")
	})
}

func (s *Server) monitorSub2ProxyReleases(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.tickSub2ProxyReleases(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) tickSub2ProxyReleases(ctx context.Context) {
	if !s.sub2ProxyReleaseMu.TryLock() {
		return
	}
	defer s.sub2ProxyReleaseMu.Unlock()
	// Small indexed batches; neither page loads nor other monitors wait here.
	for batch := 0; batch < 10 && ctx.Err() == nil; batch++ {
		rows, err := s.store.ClaimSub2ProxyReleases(time.Now())
		if err != nil {
			slog.Warn("读取到期代理解绑失败", "error", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		var wg sync.WaitGroup
		for _, b := range rows {
			wg.Add(1)
			go func(b model.Sub2ProxyBinding) { defer wg.Done(); s.processSub2ProxyRelease(ctx, b) }(b)
		}
		wg.Wait()
	}
}

func (s *Server) processSub2ProxyRelease(parent context.Context, b model.Sub2ProxyBinding) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	previousState := b.State
	now := time.Now().UTC()
	b.CheckedAt = &now
	b.Error = ""
	b.Attempts++
	finish := func(state string) { b.State = state; b.NextAt = 0; b.FinishedAt = &now }
	retry := func(err error) {
		b.State = "retry"
		delay := 30 * time.Second
		if b.Attempts == 2 {
			delay = time.Minute
		} else if b.Attempts > 2 {
			delay = 5 * time.Minute
		}
		b.NextAt = time.Now().Add(delay).UnixMilli()
		b.Error = "连接或接口异常，后台自动重试"
		var response *sub2.HTTPError
		if errors.As(err, &response) {
			b.Error = fmt.Sprintf("Sub2 HTTP %d，后台自动重试", response.Status)
		} else if errors.Is(err, context.DeadlineExceeded) {
			b.Error = "Sub2 请求超时，后台自动重试"
		}
	}
	defer func() {
		if err := s.store.FinishSub2ProxyRelease(b); err != nil {
			slog.Warn("保存代理解绑状态失败", "binding_id", b.ID, "error", err)
		} else if previousState != b.State {
			slog.Info("Sub2 代理保留状态更新", "binding_id", b.ID, "state", b.State, "attempts", b.Attempts, "error", b.Error)
		}
	}()
	// Serialize with same-site creates and retries. No database lock is held
	// while accessing Sub2. Context bounds both lock wait and remote work.
	unlock, err := s.lockSub2Push(ctx, b.Endpoint)
	if err != nil {
		retry(err)
		return
	}
	defer unlock()
	target, err := s.store.DownstreamTarget(b.TargetID)
	if err != nil {
		retry(err)
		return
	}
	endpoint, err := sub2.EndpointKey(target.Sub2.URL)
	if err != nil || endpoint != b.Endpoint || target.Provider != "sub2" {
		retry(errors.New("绑定连接不匹配"))
		return
	}
	v, _ := s.sub2ProxyReleaseClients.LoadOrStore(b.TargetID, sub2.New())
	client := v.(*sub2.Client)
	proxy, err := client.AccountProxy(ctx, target.Sub2, target.Secret, b.AccountID)
	if err != nil {
		if errors.Is(err, sub2.ErrProxyAccountDeleted) {
			finish("deleted")
			return
		}
		retry(err)
		return
	} // A generic 404 is not evidence of account deletion.
	if proxy == 0 {
		finish("unbound")
		return
	}
	if proxy != b.ProxyID {
		finish("changed")
		return
	}
	if b.Minutes == 0 {
		finish("permanent")
		b.FinishedAt = nil
		return
	}
	if b.DueAt == nil {
		retry(errors.New("缺少截止时间"))
		return
	}
	if now.Before(*b.DueAt) {
		b.State = "bound"
		b.NextAt = b.DueAt.UnixMilli()
		b.Attempts = 0
		return
	}
	// Send only the proxy field. Never change credentials, groups or scheduling.
	if err = client.ClearAccountProxy(ctx, target.Sub2, target.Secret, b.AccountID); err != nil {
		retry(err)
		return
	}
	proxy, err = client.AccountProxy(ctx, target.Sub2, target.Secret, b.AccountID)
	if err != nil {
		if errors.Is(err, sub2.ErrProxyAccountDeleted) {
			finish("deleted")
			return
		}
		retry(err)
		return
	}
	if proxy == 0 {
		finish("unbound")
	} else if proxy != b.ProxyID {
		finish("changed")
	} else {
		retry(errors.New("解绑后代理仍存在"))
	}
}
