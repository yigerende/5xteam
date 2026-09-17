package httpapi

import (
	"net/http"

	"chatgpt-space-merge/internal/model"
)

func (s *Server) updateAdminRotation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Disabled *bool `json:"disabled"`
	}
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	if input.Disabled == nil {
		writeAPI(w, http.StatusBadRequest, nil, "请指定母号启用或禁用状态")
		return
	}
	// Share the admission lock with task assignment and first invitation.
	// Already admitted work keeps running; disabling never waits for OpenAI.
	s.seatAssignmentMu.Lock()
	profile, err := s.store.UpdateAdminRotation(r.PathValue("id"), *input.Disabled)
	s.seatAssignmentMu.Unlock()
	if err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	writeAPI(w, http.StatusOK, profile, "")
}

func rotationAdminAllowed(admin model.AdminAccountProfile, account model.FreeAccountProfile, tasks []model.AutoRotationTask) bool {
	if !admin.RotationDisabled {
		return true
	}
	if account.RemoveStatus == "completed" || account.RemoteRemovedAt != nil {
		return false
	}
	if activeTeamMembership(account) && account.AdminAccountID == admin.ID && account.TeamAccountID == admin.TeamAccountID {
		return true
	}
	for _, task := range tasks {
		if task.AccountID == account.ID && task.AdminAccountID == admin.ID &&
			(task.CycleID == "" || task.CycleID == account.CycleID) &&
			(task.Status == "queued" || task.Status == "running") {
			return true
		}
	}
	return false
}

// Refresh profiles kept by a running planner so toggles take effect mid-batch.
func (s *Server) currentRotationAdmins(admins []model.AdminAccountProfile) []model.AdminAccountProfile {
	current := make(map[string]model.AdminAccountProfile)
	for _, admin := range s.store.AdminAccounts() {
		current[admin.ID] = admin
	}
	result := make([]model.AdminAccountProfile, 0, len(admins))
	for _, admin := range admins {
		if latest, ok := current[admin.ID]; ok {
			result = append(result, latest)
		}
	}
	return result
}
