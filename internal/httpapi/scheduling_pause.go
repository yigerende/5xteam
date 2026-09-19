package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/sub2"
)

var errSchedulingPauseChanged = errors.New("scheduling pause changed before removal")

func schedulingPauseEligible(p model.FreeAccountProfile, settings model.Sub2Settings) bool {
	return providerForSettings(settings) == "sub2" && settings.QuotaEnabled && settings.SchedulingPauseTimeoutSeconds > 0 &&
		p.AutoRemove && !p.Dead && p.AcceptStatus == "completed" && p.PushStatus == "completed" &&
		p.PushProvider != "cpa" && p.Sub2AccountID > 0 && p.RemoveStatus != "completed" && p.RemoteRemovedAt == nil
}

func schedulingPauseExpired(p model.FreeAccountProfile, settings model.Sub2Settings, pause *sub2.SchedulingPause) bool {
	return schedulingPauseEligible(p, settings) && pause != nil && pause.AccountID == p.Sub2AccountID &&
		pause.Platform == "openai" && pause.Type == "oauth" && strings.TrimSpace(p.Email) != "" &&
		strings.EqualFold(strings.TrimSpace(pause.Email), strings.TrimSpace(p.Email)) &&
		pause.PausedSeconds > int64(settings.SchedulingPauseTimeoutSeconds)
}

// Called under the existing account lock, as part of the quota check.
func (s *Server) removeSchedulingPauseExpired(ctx context.Context, initial model.FreeAccountProfile, settings model.Sub2Settings, password string) (model.FreeAccountProfile, bool, error) {
	if !schedulingPauseEligible(initial, settings) {
		return initial, false, nil
	}
	pause, err := s.sub2.SchedulingPause(ctx, settings, password, initial.Sub2AccountID)
	if err != nil {
		s.auditAccountEvent(ctx, initial.ID, "scheduling_pause_query_failed", "quota", "monitor_quota", "sub2", "暂停调度查询失败，本次不按暂停时长移出", map[string]any{"error": err.Error()})
		return initial, false, nil
	}
	if !schedulingPauseExpired(initial, settings, pause) {
		return initial, false, nil
	}
	guard := func(p model.FreeAccountProfile) error {
		current, currentPassword, readErr := s.store.Sub2Settings()
		if readErr != nil {
			return fmt.Errorf("%w: %v", errSchedulingPauseChanged, readErr)
		}
		p, _, readErr = s.store.FreeAccountCredential(p.ID)
		if readErr != nil || !schedulingPauseEligible(p, current) ||
			current.URL != settings.URL || current.Email != settings.Email ||
			p.CycleID != initial.CycleID || p.Sub2AccountID != initial.Sub2AccountID || p.TeamAccountID != initial.TeamAccountID {
			return errSchedulingPauseChanged
		}
		latest, readErr := s.sub2.SchedulingPause(ctx, current, currentPassword, p.Sub2AccountID)
		if readErr != nil {
			return fmt.Errorf("%w: %v", errSchedulingPauseChanged, readErr)
		}
		if !schedulingPauseExpired(p, current, latest) || !latest.SchedulingPausedAt.Equal(pause.SchedulingPausedAt) {
			return errSchedulingPauseChanged
		}
		reason := fmt.Sprintf("Sub2 连续暂停调度 %d 秒，超过配置的 %d 秒", latest.PausedSeconds, current.SchedulingPauseTimeoutSeconds)
		now := time.Now()
		_, readErr = s.store.UpdateFreeAccountCycle(p.ID, p.CycleID, func(item *model.FreeAccountProfile) {
			item.RemovalReason = reason
			item.QuotaCheckedAt = &now
		})
		if readErr != nil {
			return readErr
		}
		s.auditAccountEvent(ctx, p.ID, "scheduling_pause_remove", "remove", "monitor_quota", "sub2", reason, map[string]any{
			"sub2_account_id": p.Sub2AccountID, "scheduling_paused_at": latest.SchedulingPausedAt,
			"paused_seconds": latest.PausedSeconds, "timeout_seconds": current.SchedulingPauseTimeoutSeconds,
		})
		return nil
	}
	updated, err := s.performFreeAccountRemovalGuarded(ctx, initial.ID, false, guard)
	if errors.Is(err, errSchedulingPauseChanged) {
		s.auditAccountEvent(ctx, initial.ID, "scheduling_pause_remove_cancelled", "quota", "monitor_quota", "sub2", "暂停调度或配置已改变，或复核失败，取消本次暂停超时移出", map[string]any{"reason": err.Error()})
		return initial, false, nil
	}
	return updated, true, err
}
