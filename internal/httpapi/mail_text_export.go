package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"chatgpt-space-merge/internal/model"
)

func mailAccountTextLine(credentials model.MailAccountCredentials, includeAT, includeRT bool) (string, error) {
	fields := []string{credentials.Email}
	if strings.TrimSpace(credentials.GptPassword) != "" && strings.TrimSpace(credentials.TotpSecret) != "" {
		fields = append(fields, credentials.GptPassword, credentials.TotpSecret)
	} else {
		if strings.TrimSpace(credentials.MailPassword) != "" {
			fields = append(fields, credentials.MailPassword)
		}
		fields = append(fields, credentials.PickupURL)
	}
	if includeAT && strings.TrimSpace(credentials.AccessToken) != "" {
		fields = append(fields, credentials.AccessToken)
	}
	if includeRT && strings.TrimSpace(credentials.RefreshToken) != "" {
		fields = append(fields, credentials.RefreshToken)
	}
	for _, field := range fields {
		if strings.ContainsAny(field, "\r\n") || strings.Contains(field, "----") {
			return "", fmt.Errorf("字段包含换行或分隔符，无法按文本格式导出")
		}
	}
	return strings.Join(fields, "----"), nil
}

func (s *Server) exportMailAccountsText(w http.ResponseWriter, r *http.Request, emails []string, includeAT, includeRT bool) {
	var content strings.Builder
	missingAT, missingRT, missingPickup := 0, 0, 0
	// Read only selected records, releasing the store lock after each account.
	for index, email := range emails {
		if r.Context().Err() != nil {
			return
		}
		profile, credentials, err := s.store.MailAccountCredential(email)
		if err != nil {
			writeAPI(w, http.StatusConflict, nil, "无法读取账号 "+email+" 的凭证，未导出任何账号")
			return
		}
		if profile.ManagementScope == "pro" {
			writeAPI(w, http.StatusConflict, nil, "账号 "+email+" 已移入 Pro 管理，请重新选择")
			return
		}
		credentials.Email = profile.Email
		line, err := mailAccountTextLine(credentials, includeAT, includeRT)
		if err != nil {
			writeAPI(w, http.StatusConflict, nil, "账号 "+email+"："+err.Error())
			return
		}
		if includeAT && strings.TrimSpace(credentials.AccessToken) == "" {
			missingAT++
		}
		if includeRT && strings.TrimSpace(credentials.RefreshToken) == "" {
			missingRT++
		}
		if (credentials.GptPassword == "" || credentials.TotpSecret == "") && credentials.PickupURL == "" {
			missingPickup++
		}
		content.WriteString(line)
		content.WriteString("\r\n")
		reportMailExportProgress(r.Context(), "processing", index+1, len(emails))
	}
	reportMailExportProgress(r.Context(), "generating", len(emails), len(emails))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="mail-accounts-%s.txt"`, beijingNow().Format("20060102-150405")))
	w.Header().Set("X-Export-Count", strconv.Itoa(len(emails)))
	w.Header().Set("X-Export-Missing-AT", strconv.Itoa(missingAT))
	w.Header().Set("X-Export-Missing-RT", strconv.Itoa(missingRT))
	w.Header().Set("X-Export-Missing-Pickup", strconv.Itoa(missingPickup))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content.String()))
}
