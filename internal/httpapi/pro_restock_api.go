package httpapi

import (
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/restock"
	"chatgpt-space-merge/internal/store"
	"context"
	"errors"
	"net/http"
	"time"
)

func (s *Server) registerProRestock(mux *http.ServeMux) {
	s.registerProFallback(mux)
	s.registerProTakeover(mux)
	mux.HandleFunc("GET /api/pro-restock/records/{id}/tasks", func(w http.ResponseWriter, r *http.Request) {
		reader, err := s.store.ProListReader(r.Context())
		if err != nil {
			writeAPI(w, 500, nil, err.Error())
			return
		}
		defer reader.Close()
		items, err := reader.ProRestockDecisionTasks(r.PathValue("id"))
		if err != nil {
			writeAPI(w, 500, nil, err.Error())
			return
		}
		writeProAccountList(w, r, items)
	})
	mux.HandleFunc("GET /api/pro-restock", s.getProRestock)
	mux.HandleFunc("POST /api/pro-restock/tasks/{id}/stop", s.stopProRestockTask)
	mux.HandleFunc("PUT /api/pro-restock/config", s.saveProRestockConfig)
	mux.HandleFunc("GET /api/pro-restock/groups", s.getProRestockGroups)
	mux.HandleFunc("POST /api/pro-restock/evaluate", func(w http.ResponseWriter, r *http.Request) {
		if err := s.tickProRestock(r.Context(), true); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errProRestockBusy) {
				status = http.StatusConflict
			}
			writeAPI(w, status, nil, err.Error())
			return
		}
		s.getProRestock(w, r)
	})
	mux.HandleFunc("POST /api/pro-restock/reset-breaker", func(w http.ResponseWriter, r *http.Request) {
		s.proRestock.tick.Lock()
		defer s.proRestock.tick.Unlock()
		e := s.store.ResetProRestockBreaker()
		if e != nil {
			writeAPI(w, 500, nil, e.Error())
			return
		}
		c, _ := s.store.ProRestockConfig()
		_ = s.store.SaveProRestockDecision(restock.Decision{ID: randomRegistrationID(), At: time.Now(), Version: c.Version, Config: c, Reason: "人工重置异常熔断，原订单与在途继续保留"})
		s.getProRestock(w, r)
	})
	mux.HandleFunc("GET /api/pro-restock/records", func(w http.ResponseWriter, r *http.Request) {
		reader, e := s.store.ProListReader(r.Context())
		if e != nil {
			writeAPI(w, 500, nil, e.Error())
			return
		}
		defer reader.Close()
		page := (&Server{store: reader}).parsePagination(r)
		v, e := reader.ProRestockDecisions(page.Limit, page.Offset)
		if e != nil {
			writeAPI(w, 500, nil, e.Error())
			return
		}
		if !r.URL.Query().Has("page_size") {
			writeProAccountList(w, r, v)
			return
		}
		total, e := reader.ProRestockDecisionCount()
		if e != nil {
			writeAPI(w, 500, nil, e.Error())
			return
		}
		writeProAccountList(w, r, paginatedData(v, total, page, nil))
	})
}
func (s *Server) getProRestock(w http.ResponseWriter, r *http.Request) {
	reader, e := s.store.ProListReader(r.Context())
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	defer reader.Close()
	c, e := reader.ProRestockConfig()
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	p, _, _, e := reader.ProSettings()
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	tasks, e := reader.ProRestockTasks()
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	limits, e := reader.ProRestockLimits(time.Now())
	if e != nil {
		writeAPI(w, 500, nil, e.Error())
		return
	}
	s.proRestock.mu.Lock()
	d, snap := s.proRestock.decision, s.proRestock.lastSnapshot
	monitor := s.proRestock.monitorState
	if live := s.proRestock.liveSnapshot; live.ID != "" && live.GroupID == c.GroupID && live.Binding == restockBinding(p) {
		snap = live
	}
	for _, t := range s.proRestock.tasks {
		if t.DecisionID == "" {
			tasks = append(tasks, t)
		}
	}
	s.proRestock.mu.Unlock()
	if snap.ID == "" {
		snap, _ = reader.ProRestockSnapshot()
	}
	if d.ID == "" {
		records, _ := reader.ProRestockDecisions(1, 0)
		if len(records) > 0 {
			d = records[0]
		}
	}
	now := time.Now()
	noRestockReason := c.NoRestockReason(now)
	dailyRun, err := reader.LatestDailyEJHMerge()
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	tc, _, _ := reader.ProTakeoverConfig()
	ts, _ := reader.ProTakeoverState()
	pay, _, err := reader.GPTPaySettings()
	if err != nil {
		writeAPI(w, 500, nil, "读取开通套餐配置失败")
		return
	}
	timetable := proRestockDailyTimetable(p, pay, c, now)
	demand := c.DemandStatus(now)
	selected, payment, routeErr := store.ResolveProPurchase(p, pay, c, now)
	if routeErr == nil {
		routeErr = validatePurchaseTemplate(reader, selected.PurchaseRoute)
	}
	purchase := map[string]any{"card_provider": selected.AutoCardProvider(), "plan_code": payment.PlanCode, "template_id": ""}
	backupEnabled, backup, backupTemplate := selected.BalanceFallback()
	if backupTemplate == "" {
		backupTemplate = p.PlanTemplate(backup, payment.PlanCode)
	}
	purchase["balance_fallback"] = map[string]any{"enabled": backupEnabled, "card_provider": backup, "plan_code": payment.PlanCode, "template_id": backupTemplate, "auto_open": (backup == "ejh" && p.EJHAutoOpen) || (backup == "nexovcc" && p.NexovccAutoOpen)}
	if selected.PurchaseRoute != nil {
		purchase["template_id"] = selected.PurchaseRoute.TemplateID
	}
	if routeErr != nil {
		purchase["error"] = routeErr.Error()
	}
	provider := timetable.Current.Provider
	if provider == "" {
		provider = c.EffectiveCardProvider(now, p.AutoCardProvider())
	}
	writeProAccountList(w, r, map[string]any{"takeover": map[string]any{"config": tc, "state": ts}, "config": c, "inherited": p, "snapshot": snap, "monitor": monitor, "decision": d, "tasks": tasks, "limits": limits, "server_time": now, "no_restock_active": noRestockReason != "", "no_restock_reason": noRestockReason, "card_schedule": c.CardSchedule(now), "card_provider": provider, "purchase_timetable": timetable, "no_restock_demand": demand, "current_purchase": purchase, "daily_ejh_merge_run": dailyRun})
}
func (s *Server) saveProRestockConfig(w http.ResponseWriter, r *http.Request) {
	c := restock.DefaultConfig()
	if decodeJSON(w, r, &c, 32<<10) != nil {
		return
	}
	if e := c.Validate(); e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	if c.Enabled {
		p, password, _, e := s.store.ProSettings()
		if e == nil {
			e = validateRestockTarget(c, p)
		}
		if e == nil {
			e = s.validateProAutoPush(model.MailAccountProfile{}, p, password)
		}
		if e != nil {
			writeAPI(w, 400, nil, e.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		snap, e := s.collectProRestock(ctx, c, p, password)
		if e != nil || !snap.Valid {
			message := snap.Error
			if e != nil {
				message = e.Error()
			}
			writeAPI(w, 400, nil, message)
			return
		}
		if c.Mode == "auto" && !snap.LoadValid {
			writeAPI(w, 400, nil, snap.Error)
			return
		}
	}
	s.proRestock.tick.Lock()
	defer s.proRestock.tick.Unlock()
	s.proFallback.apply.Lock()
	defer s.proFallback.apply.Unlock()
	c, e := s.store.SaveProRestockConfig(c)
	if e != nil {
		writeAPI(w, 409, nil, e.Error())
		return
	}
	_ = s.store.SaveProRestockDecision(restock.Decision{ID: randomRegistrationID(), At: time.Now(), Version: c.Version, Config: c, Reason: "配置已保存：" + c.Mode})
	s.proRestock.next = time.Time{}
	writeAPI(w, 200, c, "")
}
func (s *Server) getProRestockGroups(w http.ResponseWriter, r *http.Request) {
	p, password, _, e := s.store.ProSettings()
	if e != nil || p.Provider != "sub2" {
		writeAPI(w, 400, nil, "请先在 Pro 推送设置选择 Sub2")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	groups, e := s.sub2.Groups(ctx, p.Sub2, password)
	if e != nil {
		writeAPI(w, 400, nil, e.Error())
		return
	}
	writeAPI(w, 200, groups, "")
}
