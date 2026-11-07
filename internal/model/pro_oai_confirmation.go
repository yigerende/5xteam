package model

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var ProDispositionModes = []string{"merge", "deactivate", "merchant", "merge_v2"}

type ProOAIInquiry struct {
	Prompt        string `json:"prompt"`
	SuccessMarker string `json:"success_marker"`
	FailureMarker string `json:"failure_marker"`
	ReviewMarker  string `json:"review_marker"`
}

func DefaultProOAIInquiry() ProOAIInquiry {
	return ProOAIInquiry{
		Prompt:        "请查询订阅邮箱【{{email}}】对应的最近一笔网页订阅退款，确认是否已经发起或处理，并给我预计入账时间和发票号。请以实际查询结果为准，不要仅凭我的描述推断；这次仅查询，不要新建退款、取消订阅或删除账户申请。\n如果确认退款已发起或已处理，请在最后一行仅输入：{{success_marker}}\n如果确认退款未发起或没有退款，请在最后一行仅输入：{{failure_marker}}\n如果无法查询或不能确认，请在最后一行仅输入：{{review_marker}}",
		SuccessMarker: "已成功", FailureMarker: "已失败", ReviewMarker: "待核实",
	}
}

func (v ProOAIInquiry) Normalized() ProOAIInquiry {
	d := DefaultProOAIInquiry()
	for _, p := range []struct {
		target   *string
		fallback string
	}{{&v.Prompt, d.Prompt}, {&v.SuccessMarker, d.SuccessMarker}, {&v.FailureMarker, d.FailureMarker}, {&v.ReviewMarker, d.ReviewMarker}} {
		*p.target = strings.TrimSpace(*p.target)
		if *p.target == "" {
			*p.target = p.fallback
		}
	}
	return v
}

func (v ProOAIInquiry) Validate() error {
	v = v.Normalized()
	if utf8.RuneCountInString(v.Prompt) > 6000 {
		return fmt.Errorf("OAI 提问内容不能超过 6000 字")
	}
	seen := map[string]bool{}
	for _, marker := range []string{v.SuccessMarker, v.FailureMarker, v.ReviewMarker} {
		if utf8.RuneCountInString(marker) > 40 || strings.ContainsAny(marker, "\r\n") || seen[marker] {
			return fmt.Errorf("OAI 结果标识须为不重复的单行文字，最多 40 字")
		}
		seen[marker] = true
	}
	return nil
}

func (v ProOAIInquiry) Question(email string) string {
	v = v.Normalized()
	return strings.NewReplacer("{{email}}", email, "{{success_marker}}", v.SuccessMarker, "{{failure_marker}}", v.FailureMarker, "{{review_marker}}", v.ReviewMarker).Replace(v.Prompt)
}

type ProOAIConfirmationRule struct {
	Enabled              bool `json:"enabled"`
	DelaySeconds         int  `json:"delay_seconds"`
	RetryIntervalSeconds int  `json:"retry_interval_seconds"`
	MaxAttempts          int  `json:"max_attempts"`
}

type ProOAIConfirmationSettings map[string]ProOAIConfirmationRule

func DefaultProOAIConfirmationSettings() ProOAIConfirmationSettings {
	v := ProOAIConfirmationSettings{}
	for _, mode := range ProDispositionModes {
		v[mode] = ProOAIConfirmationRule{DelaySeconds: 300, RetryIntervalSeconds: 300, MaxAttempts: 3}
	}
	return v
}

func (v ProOAIConfirmationSettings) Validate() error {
	for mode, rule := range v {
		switch mode {
		case "merge", "deactivate", "merchant", "merge_v2":
		default:
			return fmt.Errorf("OAI 确认流程类型无效")
		}
		if rule.DelaySeconds < 1 || rule.DelaySeconds > 604800 {
			return fmt.Errorf("OAI 询问等待时间须为 1～604800 秒")
		}
		if rule.RetryIntervalSeconds < 0 || rule.RetryIntervalSeconds > 604800 || rule.MaxAttempts < 0 || rule.MaxAttempts > 10 {
			return fmt.Errorf("OAI 重试间隔须为 1～604800 秒，总尝试次数为 1～10")
		}
	}
	return nil
}

func (v ProOAIConfirmationSettings) Normalized() ProOAIConfirmationSettings {
	out := DefaultProOAIConfirmationSettings()
	for mode := range out {
		if rule, ok := v[mode]; ok {
			if rule.DelaySeconds < 1 || rule.DelaySeconds > 604800 {
				rule.DelaySeconds = 300
			}
			if rule.RetryIntervalSeconds == 0 {
				rule.RetryIntervalSeconds = 300
			}
			if rule.MaxAttempts == 0 {
				rule.MaxAttempts = 3
			}
			out[mode] = rule
		}
	}
	return out
}

// Help confirmation remains separate from disposition success and card refund evidence.
type ProOAIConfirmation struct {
	ConfirmationSource   string               `json:"confirmation_source,omitempty"`
	OrderID              string               `json:"order_id,omitempty"`
	FreshLogin           bool                 `json:"fresh_login,omitempty"`
	SuccessMarker        string               `json:"success_marker,omitempty"`
	FailureMarker        string               `json:"failure_marker,omitempty"`
	ReviewMarker         string               `json:"review_marker,omitempty"`
	Mode                 string               `json:"mode"`
	Status               string               `json:"status"`
	DelaySeconds         int                  `json:"delay_seconds"`
	RequestedAt          *time.Time           `json:"requested_at,omitempty"`
	CompletedAt          time.Time            `json:"completed_at"`
	NotBefore            *time.Time           `json:"not_before,omitempty"`
	CheckedAt            *time.Time           `json:"checked_at,omitempty"`
	ConfirmedAt          *time.Time           `json:"confirmed_at,omitempty"`
	Error                string               `json:"error,omitempty"`
	Stage                string               `json:"stage,omitempty"`
	Steps                map[string]string    `json:"steps,omitempty"`
	StepTimes            map[string]time.Time `json:"step_times,omitempty"`
	Attempts             int                  `json:"attempts"`
	MaxAttempts          int                  `json:"max_attempts"`
	RetryIntervalSeconds int                  `json:"retry_interval_seconds"`
	NextAttemptAt        *time.Time           `json:"next_attempt_at,omitempty"`
	Generation           int                  `json:"generation"`
	Manual               bool                 `json:"manual,omitempty"`
	PreparedAt           *time.Time           `json:"prepared_at,omitempty"`
	SentAt               *time.Time           `json:"sent_at,omitempty"`
	RequestID            string               `json:"request_id,omitempty"`
	ConversationID       string               `json:"conversation_id,omitempty"`
	UserMessageID        string               `json:"user_message_id,omitempty"`
	MessageID            string               `json:"message_id,omitempty"`
	Prompt               string               `json:"prompt,omitempty"`
	Answer               string               `json:"answer,omitempty"`
	Replies              []ProOAIReply        `json:"replies,omitempty"`
}

type ProOAIReply struct {
	At        time.Time `json:"at"`
	MessageID string    `json:"message_id"`
	Answer    string    `json:"answer"`
	Status    string    `json:"status"`
}

func (v *ProOAIConfirmation) Step(stage, status string, now time.Time) {
	if v.Steps == nil {
		v.Steps = map[string]string{}
	}
	if v.StepTimes == nil {
		v.StepTimes = map[string]time.Time{}
	}
	v.Stage, v.Steps[stage], v.StepTimes[stage] = stage, status, now
}

func NewProOAIConfirmation(mode string, rule ProOAIConfirmationRule) *ProOAIConfirmation {
	rule = ProOAIConfirmationSettings{mode: rule}.Normalized()[mode]
	return &ProOAIConfirmation{Mode: mode, Status: "preparing", DelaySeconds: rule.DelaySeconds, RetryIntervalSeconds: rule.RetryIntervalSeconds, MaxAttempts: rule.MaxAttempts}
}

// RequestedAt authorizes a standalone mailbox inquiry, without claiming the
// subscription was merged, deleted, refunded, or processed by a merchant.
func (v *ProOAIConfirmation) InquiryReady() bool {
	return v != nil && (!v.CompletedAt.IsZero() || v.Manual && v.RequestedAt != nil)
}

func (v *ProOAIConfirmation) Due(now time.Time) bool {
	if !v.InquiryReady() || v.ConfirmedAt != nil || v.Status == "confirmed" {
		return false
	}
	if v.Status == "waiting" {
		return v.NotBefore != nil && !now.Before(*v.NotBefore)
	}
	if v.Attempts >= v.MaxAttempts && v.MaxAttempts > 0 {
		return false
	}
	if v.Status == "review" && v.NextAttemptAt == nil && v.CheckedAt != nil {
		interval := v.RetryIntervalSeconds
		if interval < 1 {
			interval = 300
		}
		return !now.Before(v.CheckedAt.Add(time.Duration(interval) * time.Second))
	}
	return (v.Status == "failed" || v.Status == "unconfirmed" || v.Status == "review") && v.NextAttemptAt != nil && !now.Before(*v.NextAttemptAt)
}

// Treat only the completed assistant answer as evidence. A marker in the
// user's prompt, a partial stream, or a contradictory answer cannot confirm.
var oaiRefundPositive = regexp.MustCompile(`(?:退款|退费).{0,24}(?:已发起|已处理|已完成|已成功|已退还|已退回)|已(?:发起|处理|完成).{0,16}(?:退款|退费)`)

// Help also reports a recorded refund as “已产生退款”. Keep this explicit
// wording separate from a generated invoice or a hypothetical refund.
var oaiRefundProduced = regexp.MustCompile(`已产生(?:了)?(?:一笔)?(?:退款|退费)|(?:退款|退费)(?:已经|已)产生`)
var oaiRefundProducedUncertain = regexp.MustCompile(`(?:未|没有).{0,8}产生.{0,8}(?:退款|退费)|(?:退款|退费).{0,8}(?:未产生|没有产生)|(?:如果|假如|假设|若|并非|不是|不代表|不表示|不意味着).{0,16}(?:已产生|(?:退款|退费)(?:已经|已)产生)`)
var oaiRefundUncertain = regexp.MustCompile(`(?:无法|不能|未能|尚不能|暂不能).{0,16}(?:确认|核实|查询|查看)|(?:退款|退费).{0,18}(?:未发起|未处理|不存在|未完成)|(?:没有|未找到|查不到|无).{0,12}(?:退款|退费)(?:记录|申请)|(?:尚未|还未).{0,8}(?:发起|处理|完成)|(?:如果|假如|假设).{0,16}(?:退款|退费).{0,10}(?:已发起|已处理|已完成)`)
var oaiAnswerEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

func ProOAIAnswerStatus(email, answer string, rules ...ProOAIInquiry) string {
	rule := DefaultProOAIInquiry()
	if len(rules) > 0 {
		rule = rules[0].Normalized()
	}
	lines := strings.Split(strings.TrimSpace(answer), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	// Prefer an exact custom marker, including punctuation. Tolerate the
	// usual Markdown wrapping only when it is not already an exact match.
	if last != rule.SuccessMarker && last != rule.FailureMarker && last != rule.ReviewMarker {
		last = strings.Trim(last, "* _`\r。.!！")
	}
	if last == rule.FailureMarker {
		return "unconfirmed"
	}
	if last != rule.SuccessMarker {
		return "review"
	}
	body := strings.Join(lines[:len(lines)-1], "\n")
	for _, found := range oaiAnswerEmail.FindAllString(body, -1) {
		if !strings.EqualFold(found, email) {
			return "review"
		}
	}
	if (!oaiRefundPositive.MatchString(body) && !oaiRefundProduced.MatchString(body)) || oaiRefundUncertain.MatchString(body) || oaiRefundProducedUncertain.MatchString(body) {
		return "review"
	}
	return "confirmed"
}

func (p MailAccountProfile) OAIConfirmationPrerequisite(mode string) bool {
	switch mode {
	case "merge":
		// The shared job flag also covers quota/credential refreshes. Only the
		// persisted merge steps and downstream cleanup establish completion.
		p.ProWorkflowRunning = false
		return p.ProMergeProgress() == "completed"
	case "deactivate":
		return p.DeletionConfirmed()
	case "merchant":
		return p.MerchantCompleted() && p.ProMerchant.RetiredAt != nil
	case "merge_v2":
		return p.MergeV2Completed()
	}
	return false
}

func (p MailAccountProfile) OAIAvailableModes() []string {
	var modes []string
	if !p.ProActionsAllowed() {
		return modes
	}
	for _, mode := range ProDispositionModes {
		if p.OAIConfirmationPrerequisite(mode) {
			modes = append(modes, mode)
		}
	}
	return modes
}

// Capture only a new completion, not an old account touched by a settings
// change or a quota refresh. Each mode retains its own result and delay.
func (p *MailAccountProfile) PrepareOAIConfirmation(previous MailAccountProfile, settings ProOAIConfirmationSettings, now time.Time) {
	for mode, rule := range settings.Normalized() {
		v := p.ProOAIConfirmations[mode]
		if previous.OAIConfirmationPrerequisite(mode) || !p.OAIConfirmationPrerequisite(mode) || v != nil && !v.CompletedAt.IsZero() {
			continue
		}
		if p.ProOAIConfirmations == nil {
			p.ProOAIConfirmations = map[string]*ProOAIConfirmation{}
		}
		if v != nil && v.RequestedAt != nil {
			// A manual inquiry before this final step does not confirm the new
			// outcome. Preserve its replies and schedule a new configured inquiry.
			old := v
			v = NewProOAIConfirmation(mode, rule)
			v.Generation, v.Replies = old.Generation+1, old.Replies
			v.FreshLogin = true
		}
		if v == nil {
			v = NewProOAIConfirmation(mode, rule)
		}
		v.Status, v.CompletedAt = "disabled", now
		v.OrderID = p.ProAuto.OrderID
		if rule.Enabled {
			v.Status = "waiting"
			due := now.Add(time.Duration(v.DelaySeconds) * time.Second)
			v.NotBefore = &due
			v.Step("wait", "waiting", now)
		}
		p.ProOAIConfirmations[mode] = v
	}
}
