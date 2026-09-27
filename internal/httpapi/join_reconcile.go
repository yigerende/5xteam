package httpapi

import (
	"context"
	"fmt"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

// Called while the existing account and Team locks are held. A lost approval
// response must be reconciled with OpenAI before repeating the mutation.
func (s *Server) reconcileApprovedMember(ctx context.Context, client *workflow.Client, adminToken string, admin model.AdminAccountProfile, profile model.FreeAccountProfile, seatType string, proxy map[string]any, cause error) error {
	started := time.Now()
	s.recordJoinTrace(ctx, profile.ID, profile.Email, admin.ID, "member_reconcile_start", map[string]any{
		"team_account_id": admin.TeamAccountID, "email": profile.Email, "seat_type": seatType,
		"original_error": cause.Error(), "proxy": proxy,
	})
	member, err := client.FindMemberByEmail(ctx, adminToken, admin.TeamAccountID, profile.Email)
	details := map[string]any{
		"duration_ms": time.Since(started).Milliseconds(), "team_account_id": admin.TeamAccountID,
		"email": profile.Email, "expected_seat_type": seatType, "original_error": cause.Error(), "proxy": proxy,
	}
	if err == nil {
		details["http_status"] = 200
		if member == nil {
			err = fmt.Errorf("母号成员列表未找到子号 %s", profile.Email)
		} else {
			details["user_id"], details["seat_type"], details["active"] = member.ID, member.SeatType, member.Active
			switch {
			case !member.Active:
				err = fmt.Errorf("子号 %s 的空间成员已停用", profile.Email)
			case member.SeatType != seatType:
				err = fmt.Errorf("子号已在空间，但席位为 %q，要求 %q", member.SeatType, seatType)
			}
		}
	}
	if err != nil {
		details["error"] = err.Error()
		s.recordJoinTrace(ctx, profile.ID, profile.Email, admin.ID, "member_reconcile_error", details)
		return fmt.Errorf("%w；成员核实未通过：%v", cause, err)
	}
	s.recordJoinTrace(ctx, profile.ID, profile.Email, admin.ID, "member_reconcile_success", details)
	s.auditAccountEvent(ctx, profile.ID, "join", "accept", "manual_single", "",
		"母号同意响应异常，但 OpenAI 成员查询确认子号已进入空间且席位正确，恢复同意成功", details)
	return nil
}
