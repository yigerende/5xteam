package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/workflow"
)

// Reuse the mail page's live /me check, using the global proxy. Only absent,
// malformed, expired or HTTP 401 tokens cause a new temporary-AT login.
func (s *Server) prepareSeatRecoveryAT(ctx context.Context, p model.SeatRecoveryTask) (string, error) {
	_, creds, err := s.store.MailAccountCredential(p.Email)
	if err != nil {
		return "", err
	}
	settings := s.store.Settings()
	if strings.TrimSpace(settings.ProxyURL) == "" {
		return "", errors.New("AT 检测需要已配置的全局代理")
	}
	at := strings.TrimSpace(creds.AccessToken)
	needsLogin := at == ""
	if at != "" {
		info, decodeErr := workflow.DecodeUserInfo(at)
		if decodeErr == nil && (info.UserID != p.UserID || !strings.EqualFold(info.Email, p.Email)) {
			return "", errors.New("已保存 AT 的账号身份与原临停子号不一致，未执行拉回")
		}
		var check model.AdminAccountTestResult
		expiry, hasExpiry := workflow.AccessTokenExpiry(at)
		if decodeErr != nil || (hasExpiry && !expiry.After(time.Now())) {
			check = model.AdminAccountTestResult{CheckedAt: time.Now(), HTTPStatus: http.StatusUnauthorized, Message: "已保存 AT 格式无效或已过期"}
		} else {
			s.recoveryLog(p, "检测已保存 AT（全局代理，复用邮件管理 /me 检测）")
			check = workflow.TestAdminAccount(ctx, at, "", settings)
		}
		s.recoveryLog(p, fmt.Sprintf("AT 检测：有效=%t，HTTP %d，%s", check.Valid, check.HTTPStatus, check.Message))
		if err = ctx.Err(); err != nil {
			return "", err
		}
		if check.Valid || check.HTTPStatus == http.StatusUnauthorized {
			if err = s.saveRecoveryATCheck(p, check); err != nil {
				return "", err
			}
		}
		if check.Valid {
			return "已保存 AT 检测有效，跳过登录并保留原 Session / RT", nil
		}
		if check.HTTPStatus != http.StatusUnauthorized {
			return "", fmt.Errorf("AT 检测暂未通过，未判为失效、未重复登录：%s", check.Message)
		}
		needsLogin = true
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if needsLogin {
		s.recoveryLog(p, "AT 缺失或已失效，执行临时 AT 登录并保存 AT / Session")
		if _, err = s.store.UpdateSeatRecoveryTask(p.ID, func(v *model.SeatRecoveryTask) {
			v.Status = "running"
			v.Message = "AT 缺失或失效，正在登录获取临时 AT / Session"
		}); err != nil {
			return "", err
		}
		if err = s.recoveryLogin(ctx, p); err != nil {
			return "", err
		}
	}
	return "临时 AT / Session 已保存", nil
}

func (s *Server) saveRecoveryATCheck(p model.SeatRecoveryTask, check model.AdminAccountTestResult) error {
	s.recovery.mu.Lock()
	defer s.recovery.mu.Unlock()
	if _, err := s.store.SeatRecoveryTask(p.ID); err != nil {
		return err
	}
	_, err := s.store.UpdateMailAccountATStatus(p.Email, check.CheckedAt, check.Valid, check.HTTPStatus, redactSensitiveText(check.Message))
	return err
}
