package httpapi

import "net/http"

func (s *Server) deleteNexovccUnopenedCases(w http.ResponseWriter, r *http.Request) {
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
	rows, err := s.store.NexovccCases()
	if err != nil {
		writeAPI(w, 500, nil, "读取账号回款失败")
		return
	}
	emails := []string{}
	for _, row := range rows {
		if row.State == "unopened" && row.RemovedAt == "" {
			emails = append(emails, row.Email)
		}
	}
	unlock, err := s.lockCaseDeletion(emails)
	if err != nil {
		writeAPI(w, 409, nil, err.Error())
		return
	}
	defer unlock()
	out, err := s.store.DeleteNexovccUnopenedCases()
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, "清理未开通记录失败，请刷新后重试")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPI(w, http.StatusOK, out, "")
}
