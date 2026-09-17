package httpapi

import (
	"net/http"
	"strings"

	"chatgpt-space-merge/internal/model"
)

// Test unsaved connection values without changing the active provider or account state.
// Legacy GET requests and empty POST requests still use the saved Pro connection.
func (s *Server) proConnectionSettings(w http.ResponseWriter, r *http.Request) (model.ProSettings, string, string, bool) {
	v, password, key, err := s.store.ProSettings()
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, nil, err.Error())
		return v, "", "", false
	}
	if r.Method == http.MethodPost && r.Body != nil && r.ContentLength != 0 {
		var input struct {
			Sub2         *model.Sub2Settings `json:"sub2"`
			CPA          *model.CPASettings  `json:"cpa"`
			Sub2Password string              `json:"sub2_password"`
			CPAKey       string              `json:"cpa_key"`
		}
		if err := decodeJSON(w, r, &input, 2<<20); err != nil {
			return v, "", "", false
		}
		if input.Sub2 != nil {
			v.Sub2 = *input.Sub2
		}
		if input.CPA != nil {
			v.CPA = *input.CPA
		}
		if value := strings.TrimSpace(input.Sub2Password); value != "" {
			password = value
		}
		if value := strings.TrimSpace(input.CPAKey); value != "" {
			key = value
		}
	}
	v.Sub2.URL, v.Sub2.Email = strings.TrimSpace(v.Sub2.URL), strings.TrimSpace(v.Sub2.Email)
	v.CPA.URL = strings.TrimSpace(v.CPA.URL)
	return v, password, key, true
}
