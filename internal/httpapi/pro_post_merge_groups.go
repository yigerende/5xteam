package httpapi

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"chatgpt-space-merge/internal/model"
)

// Persist before removal so a restart between OpenAI removal and the Sub2
// update retains the intended groups. Empty configuration means no change.
func (s *Server) prepareProPostMergeGroups(p model.MailAccountProfile, settings model.ProSettings) (model.MailAccountProfile, error) {
	if p.ProPostMergeGroups != nil || settings.Provider != "sub2" || len(settings.PostMergeGroupIDs) == 0 {
		return p, nil
	}
	return s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		p.ProPostMergeGroups = &model.ProPostMergeGroups{
			Status: "pending", GroupIDs: slices.Clone(settings.PostMergeGroupIDs),
			AccountID: p.Sub2AccountID, Downstream: model.ProDownstream(settings), UpdatedAt: time.Now(),
		}
	})
}

// Caller holds the existing Pro account lock. A continuation of this operation
// never logs in again, merges again, or sends another OpenAI removal request.
func (s *Server) finishProPostMergeGroups(ctx context.Context, p model.MailAccountProfile) (model.MailAccountProfile, error) {
	if !p.ProPostMergeGroupsPending() {
		return p, nil
	}
	if p.TransferStatus != "completed" || (p.RemoveStatus != "completed" && p.RemoveStatus != "team_removed") {
		return p, errors.New("空间合并并移出成功后才能调整 Sub2 分组")
	}
	target := *p.ProPostMergeGroups
	p, err := s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		p.ProPostMergeGroups.Status, p.ProPostMergeGroups.Error = "running", ""
		p.ProPostMergeGroups.UpdatedAt, p.ProLastError = time.Now(), ""
	})
	if err != nil {
		return p, err
	}
	details := map[string]any{"account_id": target.AccountID, "group_ids": target.GroupIDs}
	s.auditProEvent(p, "post_merge_groups", "running", "sub2", "移出完成，开始调整 Sub2 分组", details)
	settings, password, _, err := s.store.ProSettings()
	if err == nil {
		switch {
		case !target.Downstream.Matches(model.ProDownstream(settings)):
			err = errors.New("当前下游与移出时记录的 Sub2 不同，请恢复原下游配置后重试")
		case target.AccountID <= 0 || target.AccountID != p.Sub2AccountID || p.PushProvider == "cpa":
			err = errors.New("Sub2 账号 ID 缺失或已变化，未调整分组")
		case len(target.GroupIDs) == 0:
			err = errors.New("记录的目标分组为空，未调整原分组")
		default:
			err = proImportedDownstreamOK(p, settings)
		}
	}
	if err == nil {
		err = retryProStep(ctx, settings.RetryCount, settings.RetryIntervalSeconds, func() error {
			// Read first: a previous interrupted write may already have succeeded.
			actual, readErr := s.sub2.AccountGroups(ctx, settings.Sub2, password, target.AccountID)
			if readErr != nil {
				return readErr
			}
			wanted := slices.Clone(target.GroupIDs)
			slices.Sort(wanted)
			slices.Sort(actual)
			if slices.Equal(wanted, actual) {
				return nil
			}
			return s.sub2.SetAccountGroups(ctx, settings.Sub2, password, target.AccountID, target.GroupIDs)
		})
	}
	status, message, lastError := "completed", "Sub2 分组调整完成", ""
	if err != nil {
		err = fmt.Errorf("空间已移出，Sub2 分组调整失败（可单独重试）: %w", err)
		status, message, lastError = "failed", "空间已移出，Sub2 分组调整失败", err.Error()
		details["error"] = lastError
	}
	out, saveErr := s.store.UpdateProAccount(p.Email, func(p *model.MailAccountProfile) {
		p.ProPostMergeGroups.Status, p.ProPostMergeGroups.Error = status, lastError
		p.ProPostMergeGroups.UpdatedAt, p.ProLastError = time.Now(), lastError
	})
	s.auditProEvent(p, "post_merge_groups", status, "sub2", message, details)
	return out, errors.Join(err, saveErr)
}
