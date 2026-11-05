package httpapi

import "net/http"

func (s *Server) selectUnloggedProAccounts(w http.ResponseWriter, r *http.Request) {
	reader, err := s.store.ProListReader(r.Context())
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, "读取 Pro 账号失败")
		return
	}
	defer reader.Close()
	emails, err := reader.SelectUnloggedProAccounts(r.Context(), r.URL.Query().Get("query"))
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, "筛选未登录账号失败")
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"emails": emails}, "")
}
