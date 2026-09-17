package sub2

import (
	"chatgpt-space-merge/internal/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type RemoteQualityVerdict struct {
	Status          string     `json:"status"`
	Degraded        bool       `json:"degraded"`
	Failures        int        `json:"failures"`
	Successes       int        `json:"successes"`
	CheckedAt       *time.Time `json:"checked_at"`
	EvidenceAt      *time.Time `json:"evidence_at"`
	StreakStartedAt *time.Time `json:"streak_started_at"`
	NextAt          *time.Time `json:"next_at"`
	Error           string     `json:"error"`
}
type RemoteQualityQuestion struct {
	RemoteQualityVerdict
	QuestionID    string `json:"question_id"`
	QuestionName  string `json:"question_name"`
	Answer        string `json:"answer"`
	DurationMS    int64  `json:"duration_ms"`
	ContentPassed bool   `json:"content_passed"`
	TimePassed    bool   `json:"time_passed"`
	Reason        string `json:"reason"`
}
type RemoteQualityModel struct {
	RemoteQualityVerdict
	SentModel     string `json:"sent_model"`
	ResponseModel string `json:"response_model"`
	NoNewSamples  bool   `json:"no_new_samples"`
}
type RemoteQualityResult struct {
	AccountID int64                 `json:"account_id"`
	Revision  string                `json:"revision"`
	Version   string                `json:"version"`
	Question  RemoteQualityQuestion `json:"question"`
	Model     RemoteQualityModel    `json:"model"`
}
type RemoteQualitySnapshot struct {
	Version   int       `json:"version"`
	ServerNow time.Time `json:"server_now"`
	Settings  struct {
		Enabled           bool   `json:"enabled"`
		QuestionEnabled   bool   `json:"question_enabled"`
		ModelAuditEnabled bool   `json:"model_audit_enabled"`
		Revision          string `json:"revision"`
		FailureLimit      int    `json:"failure_limit"`
		RecoveryLimit     int    `json:"recovery_limit"`
		ModelAuditModel   string `json:"model_audit_model"`
	} `json:"settings"`
	Accounts []RemoteQualityResult `json:"accounts"`
}

func (c *Client) QualityCapabilities(ctx context.Context, s model.Sub2Settings, password string) error {
	data, err := c.doJSON(ctx, s, password, http.MethodGet, "/api/v1/admin/account-quality/capabilities", nil, nil)
	if err != nil {
		return fmt.Errorf("请先升级 Qerkai 降智检测服务: %w", err)
	}
	var v struct {
		Version     int  `json:"version"`
		MaxAccounts int  `json:"max_accounts"`
		ReadOnly    bool `json:"read_only"`
	}
	if json.Unmarshal(data, &v) != nil || v.Version != 1 || v.MaxAccounts < 100 || !v.ReadOnly {
		return errors.New("Sub2 降智结果接口版本不兼容")
	}
	return nil
}
func (c *Client) QualityResults(ctx context.Context, s model.Sub2Settings, password string, ids []int64) (RemoteQualitySnapshot, error) {
	var out RemoteQualitySnapshot
	if len(ids) < 1 || len(ids) > 100 {
		return out, errors.New("降智结果每批最多100个账号")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	data, err := c.doJSON(ctx, s, password, http.MethodPost, "/api/v1/admin/account-quality/results", map[string]any{"account_ids": ids}, nil)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	if out.Version != 1 || out.ServerNow.IsZero() || len(out.Accounts) != len(ids) {
		return out, errors.New("降智结果响应不完整或版本不兼容")
	}
	wanted := map[int64]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	for _, v := range out.Accounts {
		if !wanted[v.AccountID] {
			return out, errors.New("降智结果账号不匹配")
		}
		delete(wanted, v.AccountID)
	}
	return out, nil
}
