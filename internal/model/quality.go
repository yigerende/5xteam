package model

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const DefaultQualityPrompt = "黑色袋子里有三种口味、两种形状的糖果。圆形：苹果味7颗、桃子味9颗、西瓜味8颗；五角星形：苹果味7颗、桃子味6颗、西瓜味4颗。形状可凭手感区分，但必须提前决定取出数量，不放回盲取，不能根据形状挑选、拒收或放回，取出前不能判断口味。最少取出多少颗，才能保证同时拥有‘圆形苹果味与五角星桃子味’，或者‘圆形桃子味与五角星苹果味’中的至少一组？可以简短说明推理，最后一行严格输出 FINAL_ANSWER=整数。"

type QualityQuestion struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Prompt        string `json:"prompt"`
	Answer        string `json:"answer"`
	MatchMode     string `json:"match_mode"`
	MaxDurationMS int64  `json:"max_duration_ms"`
}

type QualitySettings struct {
	PollIntervalSeconds       int               `json:"poll_interval_seconds"`
	MaxResultAgeSeconds       int               `json:"max_result_age_seconds"`
	ConditionMode             string            `json:"condition_mode"`
	Enabled                   bool              `json:"enabled"`
	QuestionEnabled           bool              `json:"question_enabled"`
	Questions                 []QualityQuestion `json:"questions"`
	ModelAuditEnabled         bool              `json:"model_audit_enabled"`
	ModelAuditModel           string            `json:"model_audit_model"`
	ModelAuditIntervalSeconds int               `json:"model_audit_interval_seconds"`
	ModelAuditEnabledAt       *time.Time        `json:"model_audit_enabled_at,omitempty"`
	Revision                  string            `json:"revision"`
	IntervalSeconds           int               `json:"interval_seconds"`
	RetrySeconds              int               `json:"retry_seconds"`
	FailureLimit              int               `json:"failure_limit"`
	Concurrency               int               `json:"concurrency"`
	HistoryLimit              int               `json:"history_limit"`
	TimeoutSeconds            int               `json:"timeout_seconds"`
	Model                     string            `json:"model"`
	ReasoningEffort           string            `json:"reasoning_effort"`
	Prompt                    string            `json:"prompt"`
	Mode                      string            `json:"mode"`
	MatchMode                 string            `json:"match_mode"`
	Answer                    string            `json:"answer"`
	MaxDurationMS             int64             `json:"max_duration_ms"`
	Action                    string            `json:"action"`
	NormalGroupIDs            []int64           `json:"normal_group_ids"`
	DegradedGroupIDs          []int64           `json:"degraded_group_ids"`
	AutoRestore               bool              `json:"auto_restore"`
	RecoveryLimit             int               `json:"recovery_limit"`
}

func DefaultQualitySettings() QualitySettings {
	return QualitySettings{QuestionEnabled: true, Questions: DefaultQualityQuestions(), ModelAuditModel: "gpt-6-astra", ModelAuditIntervalSeconds: 60, IntervalSeconds: 300, RetrySeconds: 60, FailureLimit: 2, Concurrency: 8, HistoryLimit: 200, TimeoutSeconds: 180, Model: "gpt-6-astra", ReasoningEffort: "xhigh", Prompt: DefaultQualityPrompt, Mode: "content_time", MatchMode: "answer", Answer: "29", MaxDurationMS: 20000, Action: "kick", RecoveryLimit: 2}
}
func DefaultQualityQuestions() []QualityQuestion {
	return []QualityQuestion{
		{ID: "candy", Name: "糖果最坏情况", Enabled: true, Prompt: DefaultQualityPrompt, Answer: "29", MatchMode: "answer", MaxDurationMS: 20000},
		{ID: "clock", Name: "时钟夹角", Enabled: true, Prompt: "连续走动的指针式时钟在3点15分时，时针与分针之间较小的夹角是多少度？时针不能视为停在3点。最后一行严格输出 FINAL_ANSWER=数值，不带单位。", Answer: "7.5", MatchMode: "answer", MaxDurationMS: 20000},
		{ID: "percent", Name: "百分比变化", Enabled: true, Prompt: "一个数先增加20%，再在增加后的数值基础上减少20%，最终是原数的百分之多少？最后一行严格输出 FINAL_ANSWER=数值，不带百分号。", Answer: "96", MatchMode: "answer", MaxDurationMS: 20000},
	}
}
func (v QualitySettings) ActiveQuestions() []QualityQuestion {
	if v.Questions == nil {
		return []QualityQuestion{{ID: "legacy", Name: "原有题目", Enabled: true, Prompt: v.Prompt, Answer: v.Answer, MatchMode: v.MatchMode, MaxDurationMS: v.MaxDurationMS}}
	}
	out := []QualityQuestion{}
	for _, q := range v.Questions {
		if q.Enabled {
			out = append(out, q)
		}
	}
	return out
}
func (v QualitySettings) Validate() error {
	if v.PollIntervalSeconds != 0 && (v.PollIntervalSeconds < 10 || v.PollIntervalSeconds > 86400) {
		return errors.New("查询间隔必须为10至86400秒")
	}
	if v.MaxResultAgeSeconds != 0 && (v.MaxResultAgeSeconds < 30 || v.MaxResultAgeSeconds > 86400) {
		return errors.New("结果有效期必须为30至86400秒")
	}
	if v.ConditionMode != "" && v.ConditionMode != "any" && v.ConditionMode != "all" {
		return errors.New("异常条件组合无效")
	}
	if v.Enabled && !v.QuestionEnabled && !v.ModelAuditEnabled {
		return errors.New("请至少开启一种检测")
	}
	if v.ModelAuditIntervalSeconds < 10 || v.ModelAuditIntervalSeconds > 86400 || strings.TrimSpace(v.ModelAuditModel) == "" || len(v.ModelAuditModel) > 200 {
		return errors.New("模型检查间隔必须为10～86400秒，模型名称不能为空")
	}
	if len(v.Questions) > 50 {
		return errors.New("题库最多50题")
	}
	if v.QuestionEnabled && len(v.ActiveQuestions()) == 0 {
		return errors.New("请至少启用一道题目")
	}
	seen := map[string]bool{}
	for _, question := range v.Questions {
		if question.ID == "" || len(question.ID) > 80 || seen[question.ID] || strings.TrimSpace(question.Name) == "" || len(question.Name) > 200 {
			return errors.New("题目需要唯一ID及名称")
		}
		seen[question.ID] = true
		if strings.TrimSpace(question.Prompt) == "" || len(question.Prompt) > 15000 || len(question.Answer) > 2000 || question.MaxDurationMS < 1 || question.MaxDurationMS > 300000 {
			return errors.New("题目内容或耗时阈值无效")
		}
		if question.MatchMode != "answer" && question.MatchMode != "keyword" && question.MatchMode != "regex" {
			return errors.New("题目匹配方式无效")
		}
		if v.Mode != "time" && strings.TrimSpace(question.Answer) == "" {
			return errors.New("请填写每道题的标准答案")
		}
		if question.MatchMode == "regex" {
			if _, err := regexp.Compile(question.Answer); err != nil {
				return fmt.Errorf("题目正则无效：%w", err)
			}
		}
	}
	if v.HistoryLimit < 1 || v.HistoryLimit > 1000 {
		return errors.New("每账号检测记录显示上限必须在 1～1000")
	}
	if v.IntervalSeconds < 10 || v.IntervalSeconds > 86400 || v.RetrySeconds < 10 || v.RetrySeconds > 86400 {
		return errors.New("检测及复测间隔必须在 10～86400 秒")
	}
	if v.FailureLimit < 1 || v.FailureLimit > 20 || v.RecoveryLimit < 1 || v.RecoveryLimit > 20 {
		return errors.New("连续次数必须在 1～20")
	}
	if v.Concurrency < 1 || v.Concurrency > 8 || v.TimeoutSeconds < 5 || v.TimeoutSeconds > 300 {
		return errors.New("并发必须在 1～8，超时必须在 5～300 秒")
	}
	if v.MaxDurationMS < 1 || v.MaxDurationMS > 300000 {
		return errors.New("耗时阈值必须在 1～300000 ms")
	}
	if strings.TrimSpace(v.Model) == "" || len(v.Model) > 200 || strings.TrimSpace(v.Prompt) == "" || len(v.Prompt) > 15000 {
		return errors.New("模型、题目不能为空；题目最多 15000 字节")
	}
	switch v.ReasoningEffort {
	case "low", "medium", "high", "xhigh":
	default:
		return errors.New("推理强度无效")
	}
	switch v.Mode {
	case "content", "time", "content_time":
	default:
		return errors.New("判断方式无效")
	}
	switch v.MatchMode {
	case "answer", "keyword", "regex":
	default:
		return errors.New("内容匹配方式无效")
	}
	if v.Mode != "time" && strings.TrimSpace(v.Answer) == "" {
		return errors.New("请填写标准答案、关键词或正则")
	}
	if len(v.Answer) > 2000 {
		return errors.New("答案规则过长")
	}
	if v.MatchMode == "regex" {
		if _, err := regexp.Compile(v.Answer); err != nil {
			return fmt.Errorf("正则无效：%w", err)
		}
	}
	if v.Action != "kick" && v.Action != "groups" {
		return errors.New("异常处理方式无效")
	}
	if v.Action == "groups" {
		if len(v.NormalGroupIDs) == 0 || len(v.DegradedGroupIDs) == 0 {
			return errors.New("请选择正常和降智分组")
		}
		seen := map[int64]bool{}
		for _, id := range v.NormalGroupIDs {
			if id < 1 {
				return errors.New("分组无效")
			}
			seen[id] = true
		}
		for _, id := range v.DegradedGroupIDs {
			if id < 1 || seen[id] {
				return errors.New("正常和降智分组不能重叠")
			}
		}
	}
	return nil
}

// This state follows the child across cycles; it is not a dead-account flag.
type AccountQuality struct {
	SourceRecovered    bool            `json:"source_recovered"`
	SourceVersion      string          `json:"source_version,omitempty"`
	SourceFailureLimit int             `json:"source_failure_limit,omitempty"`
	SourceModel        string          `json:"source_model,omitempty"`
	SourceQuestionAt   *time.Time      `json:"source_question_at,omitempty"`
	SourceModelAt      *time.Time      `json:"source_model_at,omitempty"`
	SourceFresh        bool            `json:"source_fresh"`
	QuestionStatus     string          `json:"question_status,omitempty"`
	QuestionID         string          `json:"question_id,omitempty"`
	QuestionName       string          `json:"question_name,omitempty"`
	NextQuestionID     string          `json:"next_question_id,omitempty"`
	QuestionDegraded   bool            `json:"question_degraded"`
	ModelAudit         ModelAuditState `json:"model_audit"`
	Status             string          `json:"status,omitempty"`
	Failures           int             `json:"failures"`
	Successes          int             `json:"successes"`
	Degraded           bool            `json:"degraded"`
	Excluded           bool            `json:"excluded"`
	Revision           string          `json:"revision,omitempty"`
	Identity           string          `json:"identity,omitempty"`
	CheckedAt          *time.Time      `json:"checked_at,omitempty"`
	NextAt             *time.Time      `json:"next_at,omitempty"`
	DurationMS         int64           `json:"duration_ms"`
	ContentPassed      *bool           `json:"content_passed,omitempty"`
	TimePassed         *bool           `json:"time_passed,omitempty"`
	Answer             string          `json:"answer,omitempty"`
	Reason             string          `json:"reason,omitempty"`
	Error              string          `json:"error,omitempty"`
	Action             string          `json:"action,omitempty"`
	ActionStatus       string          `json:"action_status,omitempty"`
	RestoreManual      bool            `json:"restore_manual,omitempty"`
	// Persist the intended target BEFORE the remote call, so ambiguous failures
	// and process restarts can retry without losing the original group set.
	RemovedGroups  []int64 `json:"removed_groups,omitempty"`
	AddedGroups    []int64 `json:"added_groups,omitempty"`
	TargetGroups   []int64 `json:"target_groups,omitempty"`
	PlanReady      bool    `json:"plan_ready"`
	AssignGroups   []int64 `json:"assign_groups,omitempty"`
	RouteURL       string  `json:"route_url,omitempty"`
	RouteAccountID int64   `json:"route_account_id,omitempty"`
	Routed         bool    `json:"routed"`
}

type ModelAuditState struct {
	Status        string     `json:"status,omitempty"`
	Revision      string     `json:"revision,omitempty"`
	Identity      string     `json:"identity,omitempty"`
	Failures      int        `json:"failures"`
	Successes     int        `json:"successes"`
	Degraded      bool       `json:"degraded"`
	CheckedAt     *time.Time `json:"checked_at,omitempty"`
	NextAt        *time.Time `json:"next_at,omitempty"`
	Since         *time.Time `json:"since,omitempty"`
	SeenIDs       []int64    `json:"seen_ids,omitempty"`
	LatestID      int64      `json:"latest_id,omitempty"`
	SampleAt      *time.Time `json:"sample_at,omitempty"`
	SentModel     string     `json:"sent_model,omitempty"`
	ResponseModel string     `json:"response_model,omitempty"`
	Error         string     `json:"error,omitempty"`
	NoNewSamples  bool       `json:"no_new_samples"`
	ErrorCount    int        `json:"error_count"`
}

type QualitySummary struct {
	ModelNormal   int        `json:"model_normal"`
	ModelSuspect  int        `json:"model_suspect"`
	ModelErrors   int        `json:"model_errors"`
	Total         int        `json:"total"`
	Degraded      int        `json:"degraded"`
	Normal        int        `json:"normal"`
	Errors        int        `json:"errors"`
	Suspect       int        `json:"suspect"`
	Routed        int        `json:"routed"`
	ContentPassed int        `json:"content_passed"`
	TimePassed    int        `json:"time_passed"`
	Kicked        int        `json:"kicked"`
	Processed     int        `json:"processed"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
}
