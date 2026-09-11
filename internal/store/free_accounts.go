package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"chatgpt-space-merge/internal/model"
)

type FreeAccountCredentials struct {
	SourceAccessToken string
	OAuthAccessToken  string
	OAuthRefreshToken string
}

func (s *Store) Sub2Settings() (model.Sub2Settings, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := model.DefaultSub2Settings()
	var raw, encrypted string
	if err := s.db.QueryRow("SELECT profile, encrypted_password FROM sub2_settings WHERE id=1").Scan(&raw, &encrypted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return settings, "", nil
		}
		return settings, "", err
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return settings, "", err
	}
	// Profiles written before the split used quota_check_interval_seconds for
	// both probes. Detect the missing new key explicitly so an existing custom
	// interval (for example 12000s) is preserved for 401 checks as well.
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) == nil {
		if _, present := fields["status_check_interval_seconds"]; !present {
			settings.StatusCheckIntervalSeconds = settings.QuotaCheckIntervalSeconds
		}
	}
	normalizeSub2Settings(&settings)
	password, err := s.decryptOptional(encrypted)
	return settings, password, err
}

func (s *Store) SaveSub2Settings(settings model.Sub2Settings, password string) (model.Sub2Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings.URL = strings.TrimRight(strings.TrimSpace(settings.URL), "/")
	settings.Email = strings.TrimSpace(settings.Email)
	normalizeSub2Settings(&settings)
	var encrypted string
	_ = s.db.QueryRow("SELECT encrypted_password FROM sub2_settings WHERE id=1").Scan(&encrypted)
	if strings.TrimSpace(password) != "" {
		var err error
		encrypted, err = s.encrypt(strings.TrimSpace(password))
		if err != nil {
			return model.Sub2Settings{}, err
		}
	}
	settings.PasswordPresent = encrypted != ""
	raw, err := json.Marshal(settings)
	if err != nil {
		return model.Sub2Settings{}, err
	}
	_, err = s.db.Exec(`INSERT INTO sub2_settings(id, profile, encrypted_password) VALUES(1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET profile=excluded.profile, encrypted_password=excluded.encrypted_password`, string(raw), encrypted)
	return settings, err
}

func (s *Store) FreeAccounts() []model.FreeAccountProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT profile FROM free_accounts")
	if err != nil {
		return []model.FreeAccountProfile{}
	}
	defer rows.Close()
	result := make([]model.FreeAccountProfile, 0)
	for rows.Next() {
		var raw string
		var profile model.FreeAccountProfile
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &profile) == nil {
			result = append(result, profile)
		}
	}
	// Keep the rotation list stable by the time an account entered the list,
	// rather than by updated_at (which changes after every workflow stage).
	// Legacy records without imported_at fall back to CreatedAt.
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i].ImportedAt, result[j].ImportedAt
		if left.IsZero() {
			left = result[i].CreatedAt
		}
		if right.IsZero() {
			right = result[j].CreatedAt
		}
		if left.Equal(right) {
			return result[i].ID > result[j].ID
		}
		return left.After(right)
	})
	return result
}

func (s *Store) SaveImportedFreeAccount(profile model.FreeAccountProfile, accessToken string) (model.FreeAccountProfile, bool, error) {
	return s.saveImportedFreeAccount(profile, accessToken, false)
}

// SaveImportedFreeAccountOnly persists a Free account received from an
// external registration system without putting it into the Team rotation
// pipeline.  The account remains available for a later, explicit manual join.
func (s *Store) SaveImportedFreeAccountOnly(profile model.FreeAccountProfile, accessToken string) (model.FreeAccountProfile, bool, error) {
	return s.saveImportedFreeAccount(profile, accessToken, true)
}

func (s *Store) saveImportedFreeAccount(profile model.FreeAccountProfile, accessToken string, pureImport bool) (model.FreeAccountProfile, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var existingRaw string
	var existing model.FreeAccountProfile
	created := true
	if err := s.db.QueryRow("SELECT profile FROM free_accounts WHERE user_id=?", profile.UserID).Scan(&existingRaw); err == nil {
		if err := json.Unmarshal([]byte(existingRaw), &existing); err != nil {
			return model.FreeAccountProfile{}, false, err
		}
		created = false
		profile.ID = existing.ID
		profile.Status, profile.InviteStatus, profile.AcceptStatus = existing.Status, existing.InviteStatus, existing.AcceptStatus
		profile.OAuthStatus, profile.PushStatus, profile.QuotaStatus, profile.RemoveStatus = existing.OAuthStatus, existing.PushStatus, existing.QuotaStatus, existing.RemoveStatus
		profile.Dead, profile.DeadReason, profile.DeadDetectedAt = existing.Dead, existing.DeadReason, existing.DeadDetectedAt
		profile.LastError = existing.LastError
		profile.OAuthAccessTokenPresent, profile.OAuthRefreshTokenPresent = existing.OAuthAccessTokenPresent, existing.OAuthRefreshTokenPresent
		profile.AdminAccountID, profile.AdminEmail, profile.TeamAccountID, profile.SeatType = existing.AdminAccountID, existing.AdminEmail, existing.TeamAccountID, existing.SeatType
		profile.ImportMode = existing.ImportMode
		profile.OAuthAccountID = existing.OAuthAccountID
		profile.Sub2AccountID, profile.Sub2AccountName, profile.Sub2GroupID, profile.Sub2GroupName = existing.Sub2AccountID, existing.Sub2AccountName, existing.Sub2GroupID, existing.Sub2GroupName
		profile.CPAAuthFileName, profile.PushProvider = existing.CPAAuthFileName, existing.PushProvider
		profile.Sub2GroupIDs, profile.Sub2GroupNames = existing.Sub2GroupIDs, existing.Sub2GroupNames
		profile.ReloginCount, profile.ReloginFailureCount, profile.ReloginLastFailedAt = existing.ReloginCount, existing.ReloginFailureCount, existing.ReloginLastFailedAt
		profile.Quota5H, profile.Quota7D, profile.ExhaustionPolicy, profile.AutoRemove = existing.Quota5H, existing.Quota7D, existing.ExhaustionPolicy, existing.AutoRemove
		profile.TotalCostUSD, profile.CostCheckedAt = existing.TotalCostUSD, existing.CostCheckedAt
		profile.CostProvider, profile.CostDownstreamIdentity = existing.CostProvider, existing.CostDownstreamIdentity
		profile.CostDownstreamSnapshot, profile.CostByAdmin = existing.CostDownstreamSnapshot, existing.CostByAdmin
		profile.JoinedAt, profile.OAuthReadyAt, profile.PushedAt, profile.QuotaCheckedAt, profile.StatusCheckedAt, profile.RemovedAt = existing.JoinedAt, existing.OAuthReadyAt, existing.PushedAt, existing.QuotaCheckedAt, existing.StatusCheckedAt, existing.RemovedAt
		profile.ImportedAt, profile.CreatedAt = existing.ImportedAt, existing.CreatedAt
		if pureImport && strings.TrimSpace(profile.TeamAccountID) == "" && profile.AcceptStatus != "completed" {
			// Repair records created by older versions, which used pending for
			// every stage and consequently looked queued in the UI.
			profile.InviteStatus, profile.AcceptStatus = "not_started", "not_started"
			profile.OAuthStatus, profile.PushStatus = "not_started", "not_started"
			profile.QuotaStatus, profile.RemoveStatus = "not_started", "not_started"
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return model.FreeAccountProfile{}, false, err
	}
	if created {
		profile.ID = strconv.FormatInt(now.UnixNano(), 36)
		profile.Status = "imported"
		if pureImport {
			// not_started is deliberately distinct from pending: pending means
			// the stage is queued for the Team rotation workflow.
			profile.InviteStatus, profile.AcceptStatus = "not_started", "not_started"
			profile.OAuthStatus, profile.PushStatus = "not_started", "not_started"
			profile.QuotaStatus, profile.RemoveStatus = "not_started", "not_started"
		} else {
			profile.InviteStatus, profile.AcceptStatus, profile.OAuthStatus = "pending", "pending", "pending"
			profile.PushStatus, profile.QuotaStatus, profile.RemoveStatus = "pending", "pending", "pending"
		}
		profile.ExhaustionPolicy, profile.AutoRemove = "7d", true
		profile.ImportedAt, profile.CreatedAt = now, now
	}
	if pureImport {
		profile.ImportMode = "pure"
	}
	profile.SourceTokenPresent = true
	profile.UpdatedAt = now
	encrypted, err := s.encrypt(strings.TrimSpace(accessToken))
	if err != nil {
		return model.FreeAccountProfile{}, false, err
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return model.FreeAccountProfile{}, false, err
	}
	if created {
		_, err = s.db.Exec(`INSERT INTO free_accounts(id, user_id, profile, encrypted_source_token, updated_at) VALUES(?, ?, ?, ?, ?)`, profile.ID, profile.UserID, string(raw), encrypted, formatTime(now))
	} else {
		_, err = s.db.Exec(`UPDATE free_accounts SET profile=?, encrypted_source_token=?, updated_at=? WHERE id=?`, string(raw), encrypted, formatTime(now), profile.ID)
	}
	return profile, created, err
}

func (s *Store) FreeAccountCredential(id string) (model.FreeAccountProfile, FreeAccountCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, source, oauthAccess, oauthRefresh string
	if err := s.db.QueryRow(`SELECT profile, encrypted_source_token, encrypted_oauth_access_token, encrypted_oauth_refresh_token FROM free_accounts WHERE id=?`, id).Scan(&raw, &source, &oauthAccess, &oauthRefresh); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.FreeAccountProfile{}, FreeAccountCredentials{}, errors.New("Free 账号不存在")
		}
		return model.FreeAccountProfile{}, FreeAccountCredentials{}, err
	}
	var profile model.FreeAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return profile, FreeAccountCredentials{}, err
	}
	credentials := FreeAccountCredentials{}
	var err error
	if credentials.SourceAccessToken, err = s.decryptOptional(source); err != nil {
		return profile, credentials, err
	}
	if credentials.OAuthAccessToken, err = s.decryptOptional(oauthAccess); err != nil {
		return profile, credentials, err
	}
	if credentials.OAuthRefreshToken, err = s.decryptOptional(oauthRefresh); err != nil {
		return profile, credentials, err
	}
	return profile, credentials, nil
}

func (s *Store) UpdateFreeAccount(id string, mutate func(*model.FreeAccountProfile)) (model.FreeAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM free_accounts WHERE id=?", id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.FreeAccountProfile{}, errors.New("Free 账号不存在")
		}
		return model.FreeAccountProfile{}, err
	}
	var profile model.FreeAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return profile, err
	}
	if mutate != nil {
		mutate(&profile)
	}
	profile.UpdatedAt = time.Now()
	encoded, err := json.Marshal(profile)
	if err != nil {
		return profile, err
	}
	_, err = s.db.Exec("UPDATE free_accounts SET profile=?, updated_at=? WHERE id=?", string(encoded), formatTime(profile.UpdatedAt), id)
	return profile, err
}

func (s *Store) SaveFreeAccountOAuth(id, accessToken, refreshToken, accountID string) (model.FreeAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, existingAccess, existingRefresh string
	if err := s.db.QueryRow("SELECT profile, encrypted_oauth_access_token, encrypted_oauth_refresh_token FROM free_accounts WHERE id=?", id).Scan(&raw, &existingAccess, &existingRefresh); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.FreeAccountProfile{}, errors.New("Free 账号不存在")
		}
		return model.FreeAccountProfile{}, err
	}
	if strings.TrimSpace(accessToken) != "" {
		var err error
		existingAccess, err = s.encrypt(strings.TrimSpace(accessToken))
		if err != nil {
			return model.FreeAccountProfile{}, err
		}
	}
	if strings.TrimSpace(refreshToken) != "" {
		var err error
		existingRefresh, err = s.encrypt(strings.TrimSpace(refreshToken))
		if err != nil {
			return model.FreeAccountProfile{}, err
		}
	}
	var profile model.FreeAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return profile, err
	}
	now := time.Now()
	profile.OAuthAccessTokenPresent, profile.OAuthRefreshTokenPresent = existingAccess != "", existingRefresh != ""
	if strings.TrimSpace(accountID) != "" {
		profile.OAuthAccountID = strings.TrimSpace(accountID)
	}
	profile.OAuthStatus, profile.Status, profile.LastError, profile.OAuthReadyAt, profile.UpdatedAt = "completed", "oauth_ready", "", &now, now
	encoded, _ := json.Marshal(profile)
	_, err := s.db.Exec(`UPDATE free_accounts SET profile=?, encrypted_oauth_access_token=?, encrypted_oauth_refresh_token=?, updated_at=? WHERE id=?`, string(encoded), existingAccess, existingRefresh, formatTime(now), id)
	return profile, err
}

func (s *Store) DeleteFreeAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec("DELETE FROM free_accounts WHERE id=?", id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return errors.New("Free 账号不存在")
	}
	return nil
}

// DeletePureImportedFreeAccount removes only a legacy integration record that
// was created by the old turb receiver. Records that have entered a team are
// never touched.
func (s *Store) DeletePureImportedFreeAccount(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, id string
	if err := s.db.QueryRow("SELECT id, profile FROM free_accounts WHERE user_id=?", strings.TrimSpace(userID)).Scan(&id, &raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var profile model.FreeAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return err
	}
	if profile.ImportMode != "pure" || strings.TrimSpace(profile.TeamAccountID) != "" || profile.AcceptStatus == "completed" {
		return nil
	}
	_, err := s.db.Exec("DELETE FROM free_accounts WHERE id=?", id)
	return err
}

func (s *Store) decryptOptional(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return s.decrypt(value)
}

func normalizeSub2Settings(settings *model.Sub2Settings) {
	if settings.QuotaCheckIntervalSeconds < 10 {
		settings.QuotaCheckIntervalSeconds = 120
	}
	if settings.QuotaRemainingThresholdPercent < 0 {
		settings.QuotaRemainingThresholdPercent = 0
	}
	if settings.QuotaRemainingThresholdPercent > 100 {
		settings.QuotaRemainingThresholdPercent = 100
	}
	// Older profiles used quota_check_interval_seconds for both checks. When
	// loading one of those profiles, keep the existing interval for the new
	// status check so upgrading does not silently change its cadence.
	if settings.StatusCheckIntervalSeconds < 10 {
		settings.StatusCheckIntervalSeconds = settings.QuotaCheckIntervalSeconds
	}
	if settings.ReloginFailureLimit < 1 || settings.ReloginFailureLimit > 20 {
		settings.ReloginFailureLimit = 2
	}
	if settings.Priority < 1 {
		settings.Priority = 1
	}
	settings.GroupIDs = uniquePositiveInt64(settings.GroupIDs)
	if len(settings.GroupIDs) == 0 && settings.GroupID > 0 {
		settings.GroupIDs = []int64{settings.GroupID}
	}
	settings.GroupNames = uniqueTrimmedStrings(settings.GroupNames)
	settings.GroupName = strings.TrimSpace(settings.GroupName)
	if len(settings.GroupNames) == 0 && settings.GroupName != "" {
		settings.GroupNames = []string{settings.GroupName}
	}
	settings.Models = uniqueTrimmedStrings(settings.Models)
	if len(settings.GroupIDs) > 0 {
		settings.GroupID = settings.GroupIDs[0]
	} else {
		settings.GroupID = 0
	}
	if len(settings.GroupNames) > 0 {
		settings.GroupName = settings.GroupNames[0]
	} else {
		settings.GroupName = ""
	}
}

func uniquePositiveInt64(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value < 1 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueTrimmedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}
