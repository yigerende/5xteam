package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

type sub2PushAttempt struct {
	Input     sub2.CreateAccountInput `json:"input"`
	Key       string                  `json:"key"`
	Proxy     sub2.Proxy              `json:"proxy"`
	Ties      int                     `json:"ties"`
	StartedAt time.Time               `json:"started_at"`
	Created   *sub2.Account           `json:"created,omitempty"`
}

func sub2PushAttemptID(endpoint, scope string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(endpoint+"\x00"+scope)))
}

func (s *Server) lockSub2Push(ctx context.Context, endpoint string) (func(), error) {
	v, _ := s.sub2PushLocks.LoadOrStore(endpoint, make(chan struct{}, 1))
	lock := v.(chan struct{})
	select {
	case lock <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lock
			return nil, err
		}
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Binding affects new downstream accounts only. OAuth and reauthorization never
// pass through this helper. Keep the lock through count-read, choice and create.
func (s *Server) createSub2WithProxy(ctx context.Context, settings model.Sub2Settings, password string, input sub2.CreateAccountInput, scope, legacyKey string, log func(string, map[string]any)) (sub2.Account, error) {
	endpoint, err := sub2.EndpointKey(settings.URL)
	if err != nil {
		return sub2.Account{}, err
	}
	id := sub2PushAttemptID(endpoint, scope)
	raw, err := s.store.Sub2PushAttempt(id)
	if err != nil {
		return sub2.Account{}, err
	}
	if !settings.BindProxy && raw == "" {
		created, err := s.sub2.CreateAccount(ctx, settings, password, input, legacyKey)
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "idempotency") {
			return s.sub2.CreateAccount(ctx, settings, password, input, legacyKey+"-"+randomRegistrationID())
		}
		return created, err
	}
	unlock, err := s.lockSub2Push(ctx, endpoint)
	if err != nil {
		return sub2.Account{}, err
	}
	defer unlock()
	raw, err = s.store.Sub2PushAttempt(id)
	if err != nil {
		return sub2.Account{}, err
	}
	var attempt sub2PushAttempt
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &attempt); err != nil {
			return sub2.Account{}, fmt.Errorf("读取待确认 Sub2 推送失败: %w", err)
		}
		if attempt.Created != nil {
			return *attempt.Created, nil
		}
		// Do not replay after Sub2's default idempotency retention period expires.
		if time.Since(attempt.StartedAt) >= 24*time.Hour {
			return sub2.Account{}, errors.New("此前 Sub2 推送结果未确认且已超过 24 小时，请先核对下游账号，避免重复创建")
		}
		log("重试待确认推送，沿用原代理、请求内容和幂等键", map[string]any{"proxy_id": attempt.Proxy.ID, "proxy_name": attempt.Proxy.Name})
	} else {
		if err := sub2.ValidateProxySettings(settings); err != nil {
			return sub2.Account{}, err
		}
		proxies, err := s.sub2.Proxies(ctx, settings, password)
		if err != nil {
			return sub2.Account{}, fmt.Errorf("读取 Sub2 代理绑定数失败，已停止推送: %w", err)
		}
		proxy, ties, err := sub2.SelectProxy(proxies, settings.ProxyIDs, time.Now())
		if err != nil {
			return sub2.Account{}, err
		}
		input.ProxyID = &proxy.ID
		attempt = sub2PushAttempt{Input: input, Key: "space-proxy-" + randomRegistrationID(), Proxy: proxy, Ties: ties, StartedAt: time.Now()}
		encoded, err := json.Marshal(attempt)
		if err != nil {
			return sub2.Account{}, err
		}
		if err := s.store.SaveSub2PushAttempt(id, string(encoded)); err != nil {
			return sub2.Account{}, err
		}
		log("已选择绑定数最少的 Sub2 代理（并列时随机）", map[string]any{"proxy_id": proxy.ID, "proxy_name": proxy.Name, "account_count": *proxy.AccountCount, "tied_candidates": ties})
	}
	created, err := s.sub2.CreateAccount(ctx, settings, password, attempt.Input, attempt.Key)
	if err != nil {
		// Only an explicit validation/auth rejection permits a fresh allocation.
		// Timeouts, 5xx and idempotency conflicts retain the exact encrypted request.
		var response *sub2.HTTPError
		if raw == "" && errors.As(err, &response) && !strings.Contains(strings.ToLower(response.Message), "idempotency") && (response.Status == 400 || response.Status == 401 || response.Status == 403 || response.Status == 404 || response.Status == 422) {
			if clearErr := s.store.DeleteSub2PushAttempt(id); clearErr != nil {
				return created, fmt.Errorf("%w；清理推送记录失败: %v", err, clearErr)
			}
		}
		log("Sub2 绑定代理推送失败", map[string]any{"proxy_id": attempt.Proxy.ID, "error": err.Error()})
		return created, err
	}
	attempt.Created = &created
	encoded, _ := json.Marshal(attempt)
	if err := s.store.SaveSub2PushAttempt(id, string(encoded)); err != nil {
		return created, err
	}
	log("Sub2 账号已创建并绑定代理", map[string]any{"proxy_id": attempt.Proxy.ID, "proxy_name": attempt.Proxy.Name, "account_id": created.ID})
	return created, nil
}

// Called only after the local account ID has been committed successfully.
func (s *Server) completeSub2Push(settings model.Sub2Settings, scope string) {
	endpoint, err := sub2.EndpointKey(settings.URL)
	if err == nil {
		_ = s.store.DeleteSub2PushAttempt(sub2PushAttemptID(endpoint, scope))
	}
}

func (s *Server) getSub2Proxies(w http.ResponseWriter, r *http.Request) {
	settings, password, err := s.store.Sub2Settings()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	var input struct {
		Sub2     *model.Sub2Settings `json:"sub2"`
		Password string              `json:"sub2_password"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(w, r, &input, 1<<20); err != nil {
			return
		}
		if input.Sub2 != nil {
			settings = *input.Sub2
		}
		if strings.TrimSpace(input.Password) != "" {
			password = input.Password
		}
	}
	s.respondSub2Proxies(w, r, settings, password)
}

func (s *Server) getProSub2Proxies(w http.ResponseWriter, r *http.Request) {
	settings, password, _, ok := s.proConnectionSettings(w, r)
	if !ok {
		return
	}
	s.respondSub2Proxies(w, r, settings.Sub2, password)
}

func (s *Server) respondSub2Proxies(w http.ResponseWriter, r *http.Request, settings model.Sub2Settings, password string) {
	if err := validateSub2Connection(settings.URL, settings.Email, password); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	proxies, err := s.sub2.Proxies(r.Context(), settings, password)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, proxies, "")
}

func resolveSub2ProxyInput(input *sub2SettingsInput, current model.Sub2Settings) error {
	if input.BindProxy == nil {
		input.BindProxy = &current.BindProxy
	}
	if input.ProxyIDs == nil {
		input.ProxyIDs = append([]int64{}, current.ProxyIDs...)
	}
	input.ProxyIDs = uniquePositiveInt64s(input.ProxyIDs)
	return sub2.ValidateProxySettings(model.Sub2Settings{BindProxy: boolValue(input.BindProxy), ProxyIDs: input.ProxyIDs})
}
