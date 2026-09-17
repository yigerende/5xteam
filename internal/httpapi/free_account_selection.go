package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

func (s *Server) selectFreeAccountsByAdmin(w http.ResponseWriter, r *http.Request) {
	adminID := strings.TrimSpace(r.URL.Query().Get("admin_account_id"))
	if adminID == "" {
		writeAPI(w, http.StatusBadRequest, nil, "请选择母号")
		return
	}
	items, err := s.store.SelectFreeAccountsByAdmin(r.Context(), adminID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPI(w, http.StatusNotFound, nil, "母号不存在，请刷新母号列表")
		} else {
			writeAPI(w, http.StatusInternalServerError, nil, "选择母号子号失败: "+err.Error())
		}
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"items": items, "total": len(items)}, "")
}
