package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

// A manual start pins its explicit issuer for selection and optional issuance.
// It never changes the default settings or overrides an automatic restock task.
type proManualCardProviderKey struct{}

func (s *Server) restockCardSettings(ctx context.Context, cfg model.ProSettings) (model.ProSettings, bool, error) {
	if fallback, _ := ctx.Value(proBalanceFallbackKey{}).(*model.ProPurchaseRoute); fallback != nil {
		return cfg.WithPurchaseRoute(fallback), true, nil
	}
	id, _ := ctx.Value(proRestockContextKey{}).(string)
	if id == "" {
		if route, _ := ctx.Value(proPurchaseContextKey{}).(*model.ProPurchaseRoute); route != nil {
			return cfg.WithPurchaseRoute(route), true, nil
		}
		if provider, _ := ctx.Value(proManualCardProviderKey{}).(string); provider != "" {
			if provider != "ejh" && provider != "nexovcc" {
				return cfg, false, fmt.Errorf("请选择 EJH 或环球卡台")
			}
			cfg.CardProvider = provider
		}
		return cfg, false, nil
	}
	task, err := s.store.ProRestockDispatchTask(id)
	if err != nil {
		return cfg, false, err
	}
	cfg = cfg.WithPurchaseRoute(task.PurchaseRoute)
	cfg.CardProvider = task.CardProvider
	return cfg, task.StrictCardProvider, nil
}

// Preserve oldest-first order and keep scheduled/restock allocations on the
// chosen issuer even when buying new cards is disabled. Other manual cards
// retain their historical compatibility with the default EJH selection.
func (s *Server) automaticProCards(cfg model.ProSettings, strictProvider ...bool) ([]gptpay.Card, error) {
	reader, err := s.store.ProListReader(context.Background())
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if err := validatePurchaseTemplate(reader, cfg.PurchaseRoute); err != nil {
		return nil, err
	}
	cards, _, err := reader.GPTPayCards(int(^uint(0)>>1), 0, true)
	if err != nil {
		return nil, err
	}
	var matches map[string]bool
	if cfg.PurchaseRoute != nil {
		matches, err = reader.ProTemplateCards(cfg.AutoCardProvider(), cfg.PurchaseRoute.TemplateID, cfg.PurchaseRoute.PlanCode)
		if err != nil {
			return nil, err
		}
	}
	filtered := make([]gptpay.Card, 0, len(cards))
	for _, card := range cards {
		if matches != nil && !matches[card.ID] {
			continue
		}
		if store.RestockCardMatches(card, cfg.AutoCardProvider(), len(strictProvider) > 0 && strictProvider[0]) {
			filtered = append(filtered, card)
		}
	}
	return filtered, nil
}

// Serialize selection, optional issuance and reservation across manual, auto
// and scheduled starts. The database still enforces physical-card occupancy.
func (s *Server) reserveProStartCard(ctx context.Context, email, selected string, autoOpen, enforceMaximum bool, admissionChecks ...func() error) (gptpay.Card, gptpay.CardSecret, error) {
	// Both manual and automatic preparation outrank maintenance. The separate
	// manual guard below still gives interactive requests first priority.
	s.proCardPreparing.Add(1)
	defer s.proCardPreparing.Add(-1)
	automatic := ctx.Value(proRestockContextKey{}) != nil || ctx.Value(proScheduledContextKey{}) == true
	if automatic && s.manualCardPreparationPending() {
		return gptpay.Card{}, gptpay.CardSecret{}, cardPreparationWait("手动全自动正在优先准备银行卡，原补货任务等待后继续")
	}
	if err := lockProCardPreparation(ctx, &s.proCardAdmissionMu, "其他账号正在准备银行卡，本次尚未提交；等待卡片调度空闲后继续"); err != nil {
		return gptpay.Card{}, gptpay.CardSecret{}, err
	}
	defer s.proCardAdmissionMu.Unlock()
	if automatic && s.manualCardPreparationPending() {
		return gptpay.Card{}, gptpay.CardSecret{}, cardPreparationWait("手动全自动正在优先准备银行卡，原补货任务等待后继续")
	}
	var card gptpay.Card
	var secret gptpay.CardSecret
	for _, check := range admissionChecks {
		if err := check(); err != nil {
			return card, secret, err
		}
	}
	taskID, _ := ctx.Value(proRestockContextKey{}).(string)
	if taskID != "" || ctx.Value(proScheduledContextKey{}) == true {
		if reason := s.proSIWCBlock(); reason != "" {
			return card, secret, errors.New(reason)
		}
	}
	var taskProvider string
	var strictProvider bool
	if taskID != "" {
		task, err := s.store.ProRestockDispatchTask(taskID)
		if err != nil {
			return card, secret, err
		}
		taskProvider, strictProvider = task.CardProvider, task.StrictCardProvider
		if taskProvider == "nexovcc" {
			op, err := s.store.NexovccRestockOperation(taskID)
			if err == nil {
				if op.Status != "succeeded" || op.CardID == "" || (selected != "" && selected != op.CardID) {
					return card, secret, fmt.Errorf("原环球开卡尚未确认或卡片不匹配，保留原卡等待同步")
				}
				selected, autoOpen = op.CardID, false
			} else if !errors.Is(err, sql.ErrNoRows) {
				return card, secret, err
			}
		}
	} else {
		cfg, _, _, err := s.store.ProSettings()
		if err != nil {
			return card, secret, err
		}
		cfg, _, err = s.restockCardSettings(ctx, cfg)
		if err != nil {
			return card, secret, err
		}
		taskProvider = cfg.AutoCardProvider()
	}
	if profile, _, e := s.store.MailAccountCredential(email); e != nil {
		return card, secret, e
	} else if profile.IsProDead() {
		return card, secret, errProAccountDead
	} else if profile.ProRestockTaskID != "" && profile.ProRestockTaskID != taskID {
		return card, secret, fmt.Errorf("账号已由 Pro 自动补货领取，未购买新卡")
	} else if (taskID != "" || enforceMaximum) && profile.ProAuto.StopRequested {
		return card, secret, fmt.Errorf("用户已停止该账号流程，未自动购买新卡")
	}
	if err := ctx.Err(); err != nil {
		return card, secret, err
	}
	if enforceMaximum {
		pro, _, _, err := s.store.ProSettings()
		if err != nil {
			return card, secret, err
		}
		capacity, err := s.store.ProCapacity()
		if err != nil {
			return card, secret, err
		}
		if capacity.OpenedUnmerged+capacity.Pending >= pro.MaxUnmerged {
			return card, secret, fmt.Errorf("已开通和在途账号已达到最大未合并数")
		}
	}
	var err error
	if selected != "" {
		card, secret, err = s.store.GPTPayCard(selected)
		if err != nil || !card.Enabled {
			return card, secret, fmt.Errorf("请选择已启用的银行卡")
		}
		if !store.RestockCardMatches(card, taskProvider, strictProvider) {
			return card, secret, fmt.Errorf("银行卡不属于本次开通配置的卡台，请刷新后重新选择")
		}
		if card.IssuerDisabled {
			return card, secret, fmt.Errorf("该银行卡所属卡台已停用，请开启卡台后再开通 Pro")
		}
		if card.NormalPending {
			return card, secret, fmt.Errorf("银行卡已移到正常待定，请选择正常分类的可用卡片")
		}
		if err = gptpay.ValidateCard(secret, time.Now()); err != nil {
			return card, secret, err
		}
		if card.Source == "nexovcc" && (card.IssuerLocked || card.IssuerStatus != "1") {
			return card, secret, fmt.Errorf("环球卡片未激活、冻结或状态不可用")
		}
		if card.Source == "ejh" && (card.IssuerLocked || (card.IssuerStatus != "1" && card.IssuerStatus != "3")) {
			return card, secret, fmt.Errorf("卡台卡片未激活、冻结或状态不可用")
		}
		if card.IssuerEnd != "" {
			end, e := time.ParseInLocation("2006-01-02 15:04:05", card.IssuerEnd, time.FixedZone("CST", 8*3600))
			if e != nil || !end.After(time.Now()) {
				return card, secret, fmt.Errorf("卡台卡片已过期或到期时间无效")
			}
		}
		if !card.Available {
			// A previously selectable EJH card may have been claimed by another
			// task. Only this capacity/occupancy case permits automatic fallback;
			// frozen, expired and disabled cards are not silently substituted.
			if autoOpen && (card.CardType == "ejh" || card.CardType == "nexovcc") && (card.RemainingAccounts == 0 || (card.SingleActiveAccount && card.ActiveAccounts > 0)) {
				selected = ""
			} else if card.SingleActiveAccount && card.ActiveAccounts > 0 {
				return card, secret, fmt.Errorf("银行卡正在被 %s 占用（一卡同时一个号）；请启用自动补卡或等待回款确认到账", card.ActiveEmail)
			} else {
				return card, secret, fmt.Errorf("银行卡没有剩余开通名额或状态不可用")
			}
		}
	}
	if selected == "" {
		if !autoOpen {
			return card, secret, fmt.Errorf("请选择银行卡，或启用自动补卡")
		}
		card, ctx, err = s.ensureProviderCardWithFallback(ctx, admissionChecks...)
		if fallback, _ := ctx.Value(proBalanceFallbackKey{}).(*model.ProPurchaseRoute); fallback != nil {
			taskProvider, strictProvider = fallback.CardProvider, true
			if p, _, e := s.store.MailAccountCredential(email); e == nil {
				s.auditProEvent(p, "card", "running", "pro_auto", "原卡台余额不足，切换备用卡台", map[string]any{"provider": taskProvider, "template_id": fallback.TemplateID, "plan_code": fallback.PlanCode})
			}
		}
		if err != nil {
			return card, secret, err
		}
		card, secret, err = s.store.GPTPayCard(card.ID)
		if err != nil {
			return card, secret, err
		}
		if !store.RestockCardMatches(card, taskProvider, strictProvider) {
			return card, secret, fmt.Errorf("开通卡台配置已变化，请刷新后重新选择")
		}
	}
	if err = ctx.Err(); err != nil {
		return card, secret, err
	}
	cfg, _, _, e := s.store.ProSettings()
	if e != nil {
		return card, secret, e
	}
	cfg, _, e = s.restockCardSettings(ctx, cfg)
	if e != nil {
		return card, secret, e
	}
	if cfg.PurchaseRoute != nil {
		matches, e := s.store.ProTemplateCards(cfg.AutoCardProvider(), cfg.PurchaseRoute.TemplateID, cfg.PurchaseRoute.PlanCode)
		if e != nil {
			return card, secret, e
		}
		if !matches[card.ID] {
			return card, secret, fmt.Errorf("银行卡与本次套餐模板不匹配")
		}
	}
	err = s.store.ReserveProCard(email, card.ID, enforceMaximum, taskID)
	return card, secret, err
}
