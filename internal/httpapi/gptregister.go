package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"

	"chatgpt-space-merge/internal/gptregister"
	"chatgpt-space-merge/internal/model"
)

type gptRegisterRuntime struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	closed      bool
	wg          sync.WaitGroup
	paceMu      sync.Mutex
	nextRequest time.Time
}

func (s *Server) gptRegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail/gptregister/settings", s.getGPTRegisterSettings)
	mux.HandleFunc("PUT /api/mail/gptregister/settings", s.saveGPTRegisterSettings)
	mux.HandleFunc("POST /api/mail/gptregister/test", s.testGPTRegister)
	mux.HandleFunc("GET /api/mail/gptregister/catalog", s.gptRegisterCatalog)
	mux.HandleFunc("GET /api/mail/gptregister/jobs", s.listGPTRegisterJobs)
	mux.HandleFunc("POST /api/mail/gptregister/jobs", s.createGPTRegisterJob)
	mux.HandleFunc("GET /api/mail/gptregister/jobs/{id}/receipts", s.gptRegisterReceipts)
	mux.HandleFunc("POST /api/mail/gptregister/jobs/{id}/retry", s.retryGPTRegisterJob)
}
func (s *Server) getGPTRegisterSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GPTRegisterSettings()
	if err != nil {
		writeAPI(w, 500, nil, "读取注册平台配置失败")
		return
	}
	writeAPI(w, 200, cfg.Settings, "")
}
func (s *Server) saveGPTRegisterSettings(w http.ResponseWriter, r *http.Request) {
	var in gptregister.Submission
	if decodeJSON(w, r, &in, 16<<10) != nil {
		return
	}
	cfg, err := s.store.SaveGPTRegisterSettings(in)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	writeAPI(w, 200, cfg, "")
}
func (s *Server) testGPTRegister(w http.ResponseWriter, r *http.Request) {
	var in gptregister.Submission
	if decodeJSON(w, r, &in, 16<<10) != nil {
		return
	}
	cfg, err := s.store.ResolveGPTRegisterSettings(in)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	s.writeGPTRegisterCatalog(w, r, cfg)
}
func (s *Server) gptRegisterCatalog(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GPTRegisterSettings()
	if err != nil {
		writeAPI(w, 500, nil, "读取注册平台配置失败")
		return
	}
	s.writeGPTRegisterCatalog(w, r, cfg)
}
func (s *Server) writeGPTRegisterCatalog(w http.ResponseWriter, r *http.Request, cfg gptregister.Submission) {
	client, err := gptregister.New(cfg.URL, cfg.APIKey)
	if err != nil {
		writeAPI(w, 400, nil, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	client.BeforeRequest = s.paceGPTRegister
	user, err := client.Me(ctx)
	if err != nil {
		writeAPI(w, 502, nil, err.Error())
		return
	}
	products, err := client.Products(ctx)
	if err != nil {
		writeAPI(w, 502, nil, err.Error())
		return
	}
	writeAPI(w, 200, map[string]any{"user": user, "catalog": products, "settings": cfg.Settings}, "")
}
func (s *Server) createGPTRegisterJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestKey string `json:"request_key"`
		Quantity   int    `json:"quantity"`
		NeedOAuth  *bool  `json:"need_oauth"`
	}
	if decodeJSON(w, r, &in, 2048) != nil {
		return
	}
	if in.NeedOAuth == nil {
		writeAPI(w, 400, nil, "请选择是否获取 OAuth")
		return
	}
	job, err := s.store.CreateGPTRegisterJob(in.RequestKey, in.Quantity, *in.NeedOAuth)
	if err != nil {
		writeAPI(w, 400, nil, "无法创建注册任务，请检查配置、数量和请求标识（重复请求须保持原数量及 OAuth 选项）")
		return
	}
	writeAPI(w, 202, job, "")
}
func (s *Server) listGPTRegisterJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.GPTRegisterJobs()
	if err != nil {
		writeAPI(w, 500, nil, "读取注册任务失败")
		return
	}
	writeAPI(w, 200, jobs, "")
}
func (s *Server) gptRegisterReceipts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.GPTRegisterReceipts(r.PathValue("id"))
	if err != nil {
		writeAPI(w, 500, nil, "读取导入记录失败")
		return
	}
	writeAPI(w, 200, rows, "")
}
func (s *Server) retryGPTRegisterJob(w http.ResponseWriter, r *http.Request) {
	if err := s.store.RetryGPTRegisterJob(r.PathValue("id")); err != nil {
		writeAPI(w, 409, nil, "任务不存在、已完成或正在同步，请刷新列表后重试")
		return
	}
	writeAPI(w, 200, map[string]bool{"accepted": true}, "")
}

func (s *Server) paceGPTRegister(ctx context.Context) error {
	rt := &s.gptRegister
	rt.paceMu.Lock()
	defer rt.paceMu.Unlock()
	if wait := time.Until(rt.nextRequest); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	rt.nextRequest = time.Now().Add(700 * time.Millisecond)
	return nil
}
func (s *Server) startGPTRegister(ctx context.Context) {
	rt := &s.gptRegister
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed || rt.cancel != nil {
		return
	}
	ctx, rt.cancel = context.WithCancel(ctx)
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		timer := time.NewTicker(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			s.runGPTRegisterOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
}
func (s *Server) stopGPTRegister() {
	rt := &s.gptRegister
	rt.mu.Lock()
	rt.closed = true
	if rt.cancel != nil {
		rt.cancel()
	}
	rt.mu.Unlock()
	rt.wg.Wait()
}

func (s *Server) runGPTRegisterOnce(parent context.Context) {
	j, cfg, lease, err := s.store.ClaimGPTRegisterJob(time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	client, err := gptregister.New(cfg.URL, cfg.APIKey)
	if err == nil {
		client.BeforeRequest = s.paceGPTRegister
		err = s.syncGPTRegisterJob(ctx, client, &j, lease)
	}
	if err != nil {
		j.Failures++
		j.State = "retrying"
		j.Message = err.Error()
		delay := time.Duration(min(300, 5*(1<<min(j.Failures, 6)))) * time.Second
		var ae *gptregister.APIError
		if errors.As(err, &ae) {
			if ae.Status == 429 {
				delay = time.Duration(ae.RetryAfter) * time.Second
			} else if ae.Status >= 400 && ae.Status < 500 {
				j.State = "paused"
			}
		}
		var pause *gptRegisterPause
		if errors.As(err, &pause) {
			j.State = "paused"
		}
		j.NextAt = time.Now().Add(delay)
	}
	// Losing this final write is safe: the lease expires and the same durable
	// intent/order resumes, while each account has its own committed receipt.
	_ = s.store.SaveGPTRegisterJob(j, lease, true)
}

type gptRegisterPause struct{ message string }

func (e *gptRegisterPause) Error() string { return e.message }

func (s *Server) syncGPTRegisterJob(ctx context.Context, c *gptregister.Client, j *model.GPTRegisterJob, lease string) error {
	me, err := c.Me(ctx)
	if err != nil {
		return err
	}
	if j.RemoteUserID != "" && j.RemoteUserID != me.ID {
		return &gptRegisterPause{"密钥所属用户与原任务不一致，请恢复原用户的密钥后同步"}
	}
	j.RemoteUserID = me.ID
	if j.RemoteOrderID == "" {
		if !j.SubmitAttempted {
			catalog, e := c.Products(ctx)
			if e != nil {
				return e
			}
			if !catalog.OrdersOpen {
				return &gptRegisterPause{"注册平台暂未开放下单，可在开放后继续原任务"}
			}
			if j.Quantity > catalog.MaxQuantity {
				return &gptRegisterPause{"注册数量超过注册平台当前限额，请重新创建合适数量的任务"}
			}
			for _, p := range catalog.Products {
				if p.NeedOAuth == j.NeedOAuth {
					j.UnitPrice = p.UnitPrice
				}
			}
			if me.Points < int64(j.Quantity)*j.UnitPrice {
				return &gptregister.APIError{Status: 402}
			}
			j.SubmitAttempted = true
			if e = s.store.SaveGPTRegisterJob(*j, lease, false); e != nil {
				return errors.New("保存注册请求失败，尚未发送下单请求")
			}
		}
		order, e := c.Create(ctx, j.ID, j.Quantity, j.NeedOAuth)
		if e != nil {
			return e
		}
		if e = validateGPTRegisterOrder(order, j); e != nil {
			return e
		}
		j.RemoteOrderID = order.ID
		if e = s.store.SaveGPTRegisterJob(*j, lease, false); e != nil {
			return errors.New("保存平台订单失败，将使用原幂等标识恢复")
		}
	}
	order, err := c.Order(ctx, j.RemoteOrderID)
	if err != nil {
		return err
	}
	if err = validateGPTRegisterOrder(order, j); err != nil {
		return err
	}
	j.RemoteStatus = order.Status
	j.Delivered = order.Delivered
	j.UnitPrice = order.UnitPrice
	j.Charged = order.Charged
	j.Released = order.Released
	j.State = "syncing"
	j.Message = "注册进行中，交付一条导入一条"
	if order.Status == "review" {
		j.Message = "平台正在核实原订单，继续同步已交付账号"
	}
	receipts, err := s.store.GPTRegisterReceipts(j.ID)
	if err != nil {
		return errors.New("读取导入记录失败")
	}
	seen := map[string]bool{}
	for _, r := range receipts {
		seen[r.AccountID] = true
	}
	importedThisPass := 0
	var deliveryError error
	scanComplete := false
	for offset := 0; offset < j.Quantity; offset += 100 {
		page, e := c.Accounts(ctx, j.RemoteOrderID, offset)
		if e != nil {
			return e
		}
		if page.Total < 0 || page.Total > j.Quantity || len(page.Items) > 100 {
			return errors.New("注册平台交付列表数量异常")
		}
		for _, a := range page.Items {
			if a.ID == "" || a.OrderID != j.RemoteOrderID || a.NeedOAuth != j.NeedOAuth {
				return errors.New("注册平台交付与原订单不匹配")
			}
			if seen[a.ID] {
				continue
			}
			// Bound work per lease so one large order cannot starve later tasks.
			if importedThisPass >= 6 {
				break
			}
			creds, e := c.Credentials(ctx, a.ID)
			importedThisPass++
			if e != nil {
				var ae *gptregister.APIError
				if errors.As(e, &ae) && ae.Status != 404 {
					return e
				}
				deliveryError = e
				continue
			}
			if e = s.store.ImportGPTRegisterDelivery(*j, lease, a, creds); e != nil {
				deliveryError = errors.New("部分交付资料不完整或本地导入失败，已成功导入的账号保留，继续同步原任务")
				continue
			}
			seen[a.ID] = true
		}
		if importedThisPass >= 6 {
			break
		}
		if offset+len(page.Items) >= page.Total {
			scanComplete = true
			break
		}
		if len(page.Items) == 0 {
			return errors.New("注册平台交付分页不完整，稍后重查")
		}
	}
	j.Imported, j.Skipped, err = s.store.GPTRegisterCounts(j.ID)
	if err != nil {
		return errors.New("读取导入统计失败")
	}
	if deliveryError != nil {
		return deliveryError
	}
	if order.Terminal() && scanComplete && j.Imported+j.Skipped == order.Delivered {
		j.State = "completed"
		j.Message = "平台订单已结束，已交付账号同步完成"
	} else if order.Terminal() {
		j.Message = "平台订单已结束，继续同步交付账号"
	}
	j.Failures = 0
	j.NextAt = time.Now().Add(5 * time.Second)
	if order.Status == "review" {
		j.NextAt = time.Now().Add(30 * time.Second)
	}
	return nil
}

func validateGPTRegisterOrder(o gptregister.Order, j *model.GPTRegisterJob) error {
	if o.ID == "" || (j.RemoteOrderID != "" && o.ID != j.RemoteOrderID) || o.Quantity != j.Quantity || o.NeedOAuth != j.NeedOAuth || o.Delivered < 0 || o.Delivered > j.Quantity || o.UnitPrice < 0 || o.Charged < 0 || o.Released < 0 {
		return errors.New("注册平台订单响应不完整或与原任务不匹配")
	}
	switch o.Status {
	case "queued", "running", "review", "completed", "partial", "failed", "cancelled":
		return nil
	}
	return errors.New("注册平台返回未知订单状态，继续保留原任务")
}
