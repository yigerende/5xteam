package httpapi

import (
	"net/http"

	"chatgpt-space-merge/internal/store"
)

func (s *Server) selectMailAccounts(w http.ResponseWriter, r *http.Request) {
	var input struct {
		store.MailAccountSelection
		Scope      string   `json:"scope"`
		PageEmails []string `json:"page_emails"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	if input.Scope != "page" && input.Scope != "all" {
		writeAPI(w, http.StatusBadRequest, nil, "选择范围只能是本页或全部")
		return
	}
	if input.Scope == "page" {
		input.Emails = []string{}
		if len(input.PageEmails) > 500 {
			writeAPI(w, http.StatusBadRequest, nil, "本页选择最多包含 500 个账号")
			return
		}
		if len(input.PageEmails) > 0 {
			emails, err := normalizeBatchCredentialEmails(input.PageEmails)
			if err != nil {
				writeAPI(w, http.StatusBadRequest, nil, err.Error())
				return
			}
			input.Emails = emails
		}
	}
	emails, err := s.store.SelectOutsideMailAccounts(input.MailAccountSelection)
	if err != nil {
		status := http.StatusInternalServerError
		if input.ATStatus != "" && input.ATStatus != "valid" && input.ATStatus != "invalid" {
			status = http.StatusBadRequest
		}
		writeAPI(w, status, nil, "选择邮件账号失败: "+err.Error())
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"emails": emails, "total": len(emails)}, "")
}
