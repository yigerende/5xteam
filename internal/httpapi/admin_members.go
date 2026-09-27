package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chatgpt-space-merge/internal/workflow"
)

func (s *Server) adminAccountMembers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	offset, limit := 0, 25
	for key, dest := range map[string]*int{"offset": &offset, "limit": &limit} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || (key == "limit" && (value < 1 || value > 25)) {
				writeAPI(w, http.StatusBadRequest, nil, "分页参数无效，每页最多 25 条")
				return
			}
			*dest = value
		}
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if len(query) > 320 {
		writeAPI(w, http.StatusBadRequest, nil, "搜索内容过长")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	profile, credentials, err := s.currentAdminCredential(ctx, r.PathValue("id"))
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	settings, err := s.settingsForAdmin(s.store.Settings(), profile)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	client, err := workflow.NewClient(settings)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	page, err := client.ListTeamMembers(ctx, credentials.AccessToken, profile.TeamAccountID, query, offset, limit)
	if err != nil {
		writeAPI(w, http.StatusBadGateway, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{
		"items": page.Items, "total": page.Total, "offset": page.Offset, "limit": page.Limit,
		"has_more": page.HasMore, "queried_at": time.Now().UTC(), "team_account_id": profile.TeamAccountID,
	}, "")
}
