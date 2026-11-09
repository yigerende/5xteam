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
	"chatgpt-space-merge/internal/store"
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
	if log == nil {
		log = func(string, map[string]any) {}
	}
	endpoint, err := sub2.EndpointKey(settings.URL)
	if err != nil {
		return sub2.Account{}, err
	}
	id := sub2PushAttemptID(endpoint, scope)
	raw, err := s.store.Sub2PushAttempt(id)
	if err != nil {
		return sub2.Account{}, err
	}
	// Pro can change its default site before an unsubmitted push. Even with
	// binding disabled, retain uncertain creates so retries cannot move sites.
	if !settings.BindProxy && raw == "" && settings.AuthMode != "siwc" && !strings.HasPrefix(scope, "pro:") {
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
		var proxy sub2.Proxy
		var ties int
		if settings.BindProxy {
			proxies, err := s.sub2.Proxies(ctx, settings, password)
			if err != nil {
				return sub2.Account{}, fmt.Errorf("读取 Sub2 代理绑定数失败，已停止推送: %w", err)
			}
			if err = s.decorateSub2ProxyUsage(endpoint, proxies); err != nil {
				return sub2.Account{}, err
			}
			proxy, ties, err = sub2.SelectProxy(proxies, settings.ProxyIDs, time.Now())
			if err != nil {
				return sub2.Account{}, err
			}
			input.ProxyID = &proxy.ID
		}
		attempt = sub2PushAttempt{Input: input, Key: "space-proxy-" + randomRegistrationID(), Proxy: proxy, Ties: ties, StartedAt: time.Now()}
		encoded, err := json.Marshal(attempt)
		if err != nil {
			return sub2.Account{}, err
		}
		if proxy.BindingKey != "" {
			err = s.store.ReserveSub2ProxyBinding(id, string(encoded), endpoint, proxy.BindingKey, attempt.Key)
		} else {
			err = s.store.SaveSub2PushAttempt(id, string(encoded))
		}
		if err != nil {
			return sub2.Account{}, err
		}
		if settings.BindProxy {
			log("已选择当前绑定账号最少、再按累计绑定次数最少的 Sub2 代理（含待确认预占，两项相同时随机）", map[string]any{"proxy_id": proxy.ID, "proxy_name": proxy.Name, "account_count": *proxy.AccountCount, "cumulative_bindings": proxy.CumulativeBindings, "pending_bindings": proxy.PendingBindings, "tied_candidates": ties})
		}
	}
	created, err := s.sub2.CreateAccount(ctx, settings, password, attempt.Input, attempt.Key)
	if err != nil {
		// Only an explicit validation/auth rejection permits a fresh allocation.
		// Timeouts, 5xx and idempotency conflicts retain the exact encrypted request.
		var response *sub2.HTTPError
		if raw == "" && errors.As(err, &response) && !strings.Contains(strings.ToLower(response.Message), "idempotency") && (response.Status == 400 || response.Status == 401 || response.Status == 403 || response.Status == 404 || response.Status == 422) {
			if clearErr := s.store.RejectSub2ProxyBinding(id, attempt.Key); clearErr != nil {
				return created, fmt.Errorf("%w；清理推送记录失败: %v", err, clearErr)
			}
		}
		log("Sub2 推送失败", map[string]any{"proxy_id": attempt.Proxy.ID, "error": err.Error()})
		return created, err
	}
	attempt.Created = &created
	encoded, _ := json.Marshal(attempt)
	if err := s.store.CompleteSub2ProxyBinding(id, string(encoded), attempt.Key, created.ID); err != nil {
		return created, err
	}
	message := "Sub2 账号已创建"
	if attempt.Input.ProxyID != nil {
		message += "并绑定代理"
	}
	log(message, map[string]any{"proxy_id": attempt.Proxy.ID, "proxy_name": attempt.Proxy.Name, "account_id": created.ID})
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
	endpoint, err := sub2.EndpointKey(settings.URL)
	if err == nil {
		err = s.decorateSub2ProxyUsage(endpoint, proxies)
	}
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, proxies, "")
}

func (s *Server) decorateSub2ProxyUsage(endpoint string, proxies []sub2.Proxy) error {
	limit, err := s.store.Sub2ProxyBindingLimit(endpoint)
	if err != nil {
		return err
	}
	baselines := map[string]int64{}
	seen := map[int64]bool{}
	for _, p := range proxies {
		if !seen[p.ID] && p.BindingKey != "" && p.AccountCount != nil && *p.AccountCount >= 0 {
			baselines[p.BindingKey] += int64(*p.AccountCount)
		}
		seen[p.ID] = true
	}
	usage, err := s.store.Sub2ProxyUsages(endpoint, baselines)
	if err != nil {
		return fmt.Errorf("读取代理累计绑定次数失败，已停止推送: %w", err)
	}
	for i := range proxies {
		p := &proxies[i]
		u := usage[p.BindingKey]
		p.CumulativeBindings, p.PendingBindings, p.BindingRevision, p.BindingSince, p.BindingLimit = u.Count, u.Pending, u.Revision, u.Since, limit
	}
	return nil
}

func (s *Server) getSub2ProxyBindingPolicy(w http.ResponseWriter, r *http.Request) {
	endpoint, err := sub2.EndpointKey(r.URL.Query().Get("url"))
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	limit, err := s.store.Sub2ProxyBindingLimit(endpoint)
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]int64{"max_bindings": limit}, "")
}

func (s *Server) saveSub2ProxyBindingPolicy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL string `json:"url"`
		Max *int64 `json:"max_bindings"`
	}
	if decodeJSON(w, r, &input, 8<<10) != nil {
		return
	}
	endpoint, err := sub2.EndpointKey(input.URL)
	if err != nil || input.Max == nil {
		writeAPI(w, 400, nil, "请填写下游地址和累计绑定上限")
		return
	}
	if err = s.store.SaveSub2ProxyBindingLimit(endpoint, *input.Max); err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]int64{"max_bindings": *input.Max}, "")
}

func (s *Server) editSub2ProxyBindingUsage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL      string `json:"url"`
		Key      string `json:"binding_key"`
		Count    *int64 `json:"count"`
		Revision *int64 `json:"expected_revision"`
	}
	if decodeJSON(w, r, &input, 8<<10) != nil {
		return
	}
	endpoint, err := sub2.EndpointKey(input.URL)
	if err != nil || input.Count == nil || input.Revision == nil {
		writeAPI(w, 400, nil, "请填写下游地址、累计次数及当前版本")
		return
	}
	err = s.store.SetSub2ProxyUsage(endpoint, input.Key, *input.Count, *input.Revision)
	if err != nil {
		status := 400
		if errors.Is(err, store.ErrSub2ProxyUsageChanged) {
			status = 409
		}
		writeAPI(w, status, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]bool{"saved": true}, "")
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
