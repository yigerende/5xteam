package httpapi

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

// Hash the connection scope, not its secret; IDs may repeat on another server.
func proCostConnectionIdentity(settings model.Sub2Settings) string {
	value := strings.TrimRight(strings.TrimSpace(settings.URL), "/") + "|" + strings.ToLower(strings.TrimSpace(settings.Email))
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func proCostBindingMatches(p model.MailAccountProfile, settings model.ProSettings) bool {
	return p.VM2 == nil && p.PushProvider == "sub2" && settings.Provider == "sub2" &&
		p.CostProvider == "sub2" && p.Sub2AccountID > 0 &&
		p.CostDownstreamIdentity == strconv.FormatInt(p.Sub2AccountID, 10) &&
		p.CostConnectionIdentity == proCostConnectionIdentity(settings.Sub2)
}

func (s *Server) saveProCostSnapshot(profile model.MailAccountProfile, settings model.ProSettings, costs sub2.AccountCosts) (model.MailAccountProfile, error) {
	now := time.Now()
	identity := strconv.FormatInt(profile.Sub2AccountID, 10)
	connection := proCostConnectionIdentity(settings.Sub2)
	return s.store.UpdateProAccount(profile.Email, func(p *model.MailAccountProfile) {
		// Ignore a response that raced with a provider/account reassignment.
		if p.PushProvider != "sub2" || p.Sub2AccountID != profile.Sub2AccountID || p.VM2 != nil {
			return
		}
		if p.ProRestockTargetID != profile.ProRestockTargetID || p.ProPushDownstream != nil && !p.ProPushDownstream.Matches(model.ProDownstream(settings)) {
			return
		}
		if p.ProSIWC != nil && p.ProSIWC.AccountID > 0 && (settings.Sub2.AuthMode != "siwc" || p.ProPushDownstream == nil || !p.ProPushDownstream.Matches(model.ProDownstream(settings))) {
			return
		}
		if !proCostBindingMatches(*p, settings) {
			p.ClearProCostSnapshot()
		}
		p.CostProvider, p.CostConnectionIdentity, p.CostDownstreamIdentity = "sub2", connection, identity
		if p.ProSIWC != nil && p.ProSIWC.AccountID == profile.Sub2AccountID && settings.Sub2.AuthMode == "siwc" {
			v := p.ProSIWC
			v.NewCost = max(v.NewCost, costs.StandardCostUSD-v.BaselineCost, 0)
			p.CostDownstreamSnapshot = v.CarriedCost + v.NewCost
			if costs.UserCostUSD != nil {
				v.NewUserCost = max(v.NewUserCost, *costs.UserCostUSD-v.BaselineUserCost, 0)
				p.UserCostDownstreamIdentity, p.UserCostDownstreamSnapshot = identity, v.CarriedUserCost+v.NewUserCost
			}
			p.CostCheckedAt = &now
			return
		}
		// Match Team's monotonic snapshot within the same downstream binding.
		p.CostDownstreamSnapshot = max(p.CostDownstreamSnapshot, costs.StandardCostUSD, 0)
		if costs.UserCostUSD != nil {
			p.UserCostDownstreamIdentity = identity
			p.UserCostDownstreamSnapshot = max(p.UserCostDownstreamSnapshot, *costs.UserCostUSD, 0)
		}
		p.CostCheckedAt = &now
	})
}
