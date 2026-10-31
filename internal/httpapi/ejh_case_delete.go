package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
)

func (s *Server) deleteEJHUnopenedCases(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if decodeJSON(w, r, &in, 8<<10) != nil {
		return
	}
	if !in.Confirm {
		writeAPI(w, http.StatusBadRequest, nil, "请确认删除未开通的账号回款记录")
		return
	}
	rows, err := s.store.EJHCases()
	if err != nil {
		writeAPI(w, 500, nil, "读取账号回款失败")
		return
	}
	emails := []string{}
	for _, row := range rows {
		if row.CardType == "ejh" && row.State == "unopened" && row.RemovedAt == nil && (r.PathValue("id") == "" || row.ID == r.PathValue("id")) {
			emails = append(emails, row.Email)
		}
	}
	unlock, err := s.lockCaseDeletion(emails)
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	defer unlock()
	ids, err := s.store.DeleteEJHUnopenedCases(r.PathValue("id"))
	if err != nil {
		code := http.StatusConflict
		if errors.Is(err, sql.ErrNoRows) {
			code = http.StatusNotFound
		}
		writeAPI(w, code, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"deleted": len(ids), "deleted_ids": ids}, "")
}
