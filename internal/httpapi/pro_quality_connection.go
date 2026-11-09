package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"chatgpt-space-merge/internal/model"
)

var errProQualityConnectionRecovered = errors.New("测试连接已恢复正常，取消本次降智处理")
var errProQualityConnectionUnconfirmed = errors.New("复核未命中降智状态码，取消本次降智处理并等待下次检测")

// Keep a successful check for the same cached error without manufacturing new
// failures or extending the check's freshness on every poll.
func restoreProQualityConnection(state *model.ProQualityState, q model.ProQualitySettings, sample string, now time.Time) bool {
	check := state.ConnectionCheck
	if check == nil || check.Status != "normal" || sample == "" || check.Sample != sample {
		return false
	}
	until := check.CheckedAt.Add(time.Duration(q.MaxResultAgeSeconds) * time.Second)
	if !now.Before(until) || check.CheckedAt.After(now.Add(5*time.Second)) {
		return false
	}
	state.Status, state.Error, state.Reason = "normal", "", "失败达限后测试连接正常，连续失败已清零"
	state.ErrorFailures, state.ErrorDegraded, state.Degraded = 0, false, false
	state.SourceFresh, state.SourceRecovered, state.ValidUntil = true, true, &until
	if !state.ActionStarted && (state.ActionStatus == "pending" || state.ActionStatus == "running" || state.ActionStatus == "failed") {
		state.ActionStatus, state.ActionError = "cancelled", ""
		state.MergeAttempts, state.NextMergeAt = 0, nil
	}
	return true
}

func (s *Server) recheckProQualityFailure(ctx context.Context, p model.MailAccountProfile, q model.ProQualitySettings, c model.ProSettings, password string, state model.ProQualityState) (model.ProQualityState, error) {
	if !state.ErrorDegraded || state.ActionStarted {
		return state, nil
	}
	probe, err := s.sub2.ProbeConnection(ctx, c.Sub2, password, p.Sub2AccountID)
	if ctx.Err() != nil {
		return state, ctx.Err()
	}
	now := time.Now().UTC()
	check := &model.ProQualityConnectionCheck{CheckedAt: now, Sample: state.ErrorSample, Status: "error", HTTPStatus: probe.HTTPStatus}
	state.ConnectionCheck = check
	if err == nil && probe.Complete && probe.HTTPStatus == http.StatusOK && probe.Error == "" {
		check.Status = "normal"
		restoreProQualityConnection(&state, q, state.ErrorSample, now)
		return state, nil
	}
	message := probe.Error
	if err != nil {
		message = err.Error()
	}
	if message == "" {
		message = "测试连接未成功完成"
	}
	if err == nil && isDeadOAuthResult(nil, probe.Error) && p.CanMarkProDead() {
		if _, saveErr := s.markProDead(p.Email, "sub2_connection", probe.Error); saveErr != nil {
			return state, saveErr
		}
		return state, errProAccountDead
	}
	message = redactSensitiveText(message)
	if chars := []rune(message); len(chars) > 240 {
		message = string(chars[:240]) + "…"
	}
	check.Error = message
	// Only a parsed upstream status is evidence. Transport/admin API failures
	// and incomplete streams have no reliable account-level status to match.
	if err != nil || !q.ConnectionFailureMatches(probe.HTTPStatus) {
		check.Status = "unconfirmed"
		state.Status, state.Reason = "error", errProQualityConnectionUnconfirmed.Error()
		state.Error = "测试连接复核未确认降智：" + message
		state.ErrorDegraded, state.Degraded, state.SourceFresh, state.SourceRecovered = false, false, false, false
		state.ValidUntil = nil
		if !state.ActionStarted && (state.ActionStatus == "pending" || state.ActionStatus == "running" || state.ActionStatus == "failed") {
			state.ActionStatus, state.ActionError = "cancelled", ""
			state.MergeAttempts, state.NextMergeAt = 0, nil
		}
		return state, nil
	}
	until := now.Add(time.Duration(q.MaxResultAgeSeconds) * time.Second)
	state.ValidUntil = &until
	state.Reason = fmt.Sprintf("连续检测失败 %d 次，复核 HTTP %d 命中配置，按降智处理", state.ErrorFailures, probe.HTTPStatus)
	state.Error = "测试连接复核失败：" + message
	return state, nil
}
