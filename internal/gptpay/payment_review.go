package gptpay

import "strings"

const PaymentReviewMessage = "付款风控待核实：已停止自动重试，保留原账号和订单，不标记死号；请核实后手动处理"

const PaymentReviewRestockMessage = "付款风控订单已确认失败：原账号保留待核实，等待一次新账号替补；替补再次风控将暂停自动补货"

// Review still prevents retrying this account. A settled, explicitly failed
// supplier order is not incoming capacity and must not hold the whole queue.
// Missing/conflicting evidence and supplier retry prohibitions retain the hold.
func (o Order) PaymentReviewBlocksRestock() bool {
	return o.PaymentReviewRequired() && !(o.Remote.ID != "" &&
		strings.EqualFold(strings.TrimSpace(o.Remote.Status), "failed") &&
		strings.EqualFold(strings.TrimSpace(o.Remote.SettlementStatus), "settled") &&
		(o.Remote.Retryable == nil || *o.Remote.Retryable))
}

// A payment risk rejection is not evidence that the account is deactivated.
// Keep the supplier's failed order intact, but require operator review before
// automation can continue. Never reinterpret successful or uncertain payments.
func (o Order) PaymentReviewRequired() bool {
	if o.OccupancyResolution != nil {
		return false
	}
	if o.Status != "failed" || o.SucceededAt != nil {
		return false
	}
	for _, value := range []string{o.Remote.ErrorCode, o.Remote.FailureCategory} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "payment_risk", "payment_risk_blocked", "risk_blocked", "fraudulent", "fraud_detected":
			return true
		}
	}
	message := strings.ToLower(o.Error + " " + o.Remote.FailureReason)
	for _, phrase := range []string{"支付风控拦截", "付款风控拦截", "payment blocked by risk", "payment blocked by fraud", "payment blocked due to fraud"} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}
