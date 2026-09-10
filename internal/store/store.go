package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"

	_ "modernc.org/sqlite"
)

type diskState struct {
	Settings model.Settings       `json:"settings"`
	Proxies  []model.ProxyProfile `json:"proxies"`
	Accounts []storedAdminAccount `json:"admin_accounts"`
	History  []model.HistoryEntry `json:"history"`
}

type storedAdminAccount struct {
	Profile               model.AdminAccountProfile `json:"profile"`
	EncryptedToken        string                    `json:"encrypted_token"`
	EncryptedRefreshToken string                    `json:"encrypted_refresh_token,omitempty"`
}

type AdminAccountCredentials struct {
	AccessToken  string
	RefreshToken string
}

type Store struct {
	mu   sync.Mutex
	path string
	key  []byte
	db   *sql.DB
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	key, err := loadMasterKey(dataDir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "state.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{path: path, key: key, db: db}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureOpenAIRefreshColumn(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.initialize(dataDir); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.EnsureAdminUser(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initSchema() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS settings (
			id INTEGER PRIMARY KEY CHECK (id = 1), payload TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS proxies (
			id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE, url TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS admin_accounts (
			id TEXT PRIMARY KEY, label TEXT NOT NULL COLLATE NOCASE UNIQUE, profile TEXT NOT NULL,
			encrypted_access_token TEXT NOT NULL, encrypted_refresh_token TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS openai_accounts (
			id TEXT PRIMARY KEY, label TEXT NOT NULL COLLATE NOCASE UNIQUE, profile TEXT NOT NULL,
			encrypted_access_token TEXT NOT NULL, encrypted_refresh_token TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS mail_accounts (
			id TEXT PRIMARY KEY, email TEXT NOT NULL COLLATE NOCASE UNIQUE, label TEXT NOT NULL DEFAULT '',
			group_name TEXT NOT NULL DEFAULT '', profile TEXT NOT NULL,
			encrypted_credentials TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS mail_messages (
			id TEXT PRIMARY KEY, account TEXT NOT NULL, payload TEXT NOT NULL, received_at TEXT NOT NULL,
			UNIQUE(account, id)
		);
		CREATE TABLE IF NOT EXISTS sms_phones (
			id TEXT PRIMARY KEY, provider TEXT NOT NULL, phone_number TEXT NOT NULL DEFAULT '',
			card_code TEXT NOT NULL DEFAULT '', api_url TEXT NOT NULL DEFAULT '',
			max_bindings INTEGER NOT NULL DEFAULT 3, status TEXT NOT NULL DEFAULT 'active',
			lease_expires_at TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
			last_code TEXT NOT NULL DEFAULT '', last_code_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '', last_attempt_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS sms_phone_bindings (
			phone_id TEXT NOT NULL, gpt_email TEXT NOT NULL, phone_number TEXT NOT NULL DEFAULT '',
			bound_at TEXT NOT NULL, PRIMARY KEY (phone_id, gpt_email)
		);
		CREATE TABLE IF NOT EXISTS sms_platform_configs (
			provider TEXT PRIMARY KEY, enabled INTEGER NOT NULL DEFAULT 0, payload TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS history (
			id TEXT PRIMARY KEY, completed_at TEXT NOT NULL, payload TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS account_progress (
			team_account_id TEXT NOT NULL, user_id TEXT NOT NULL, payload TEXT NOT NULL, updated_at TEXT NOT NULL,
			PRIMARY KEY (team_account_id, user_id)
		);
		CREATE TABLE IF NOT EXISTS sub2_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1), profile TEXT NOT NULL, encrypted_password TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS cpa_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1), profile TEXT NOT NULL, encrypted_key TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS pro_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1), profile TEXT NOT NULL,
			encrypted_sub2_password TEXT NOT NULL DEFAULT '', encrypted_cpa_key TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS pro_oauth_sessions (
			id TEXT PRIMARY KEY, profile TEXT NOT NULL, encrypted_verifier TEXT NOT NULL,
			expires_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS pro_oauth_sessions_expires_at_idx ON pro_oauth_sessions(expires_at);
		CREATE TABLE IF NOT EXISTS free_accounts (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL UNIQUE, profile TEXT NOT NULL,
			encrypted_source_token TEXT NOT NULL, encrypted_oauth_access_token TEXT NOT NULL DEFAULT '',
			encrypted_oauth_refresh_token TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS auto_rotation_settings (
			id INTEGER PRIMARY KEY CHECK (id=1), payload TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS auto_rotation_runs (
			id TEXT PRIMARY KEY, payload TEXT NOT NULL, started_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS auto_rotation_tasks (
			id TEXT PRIMARY KEY, run_id TEXT NOT NULL, account_id TEXT NOT NULL, payload TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS auto_rotation_seat_reservations (
			id TEXT PRIMARY KEY, admin_account_id TEXT NOT NULL, account_id TEXT NOT NULL UNIQUE, seat_type TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS auto_rotation_claims (
			account_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, created_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS auto_rotation_events (
			id TEXT PRIMARY KEY, run_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '', account_id TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL, created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS auto_rotation_events_created_at_idx ON auto_rotation_events(created_at DESC);
		CREATE TABLE IF NOT EXISTS admin_capacity_snapshots (
			admin_account_id TEXT PRIMARY KEY, payload TEXT NOT NULL, fetched_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS app_users (
			username TEXT PRIMARY KEY, password_hash TEXT NOT NULL, password_salt TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS history_completed_at_idx ON history(completed_at DESC);
		CREATE INDEX IF NOT EXISTS account_progress_updated_at_idx ON account_progress(updated_at DESC);
		CREATE INDEX IF NOT EXISTS free_accounts_updated_at_idx ON free_accounts(updated_at DESC);
	`)
	return err
}

func (s *Store) SaveMailMessage(message model.MailMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if message.ID == "" {
		message.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if message.ReceivedAt.IsZero() {
		message.ReceivedAt = time.Now().UTC()
	}
	b, _ := json.Marshal(message)
	_, err := s.db.Exec(`INSERT INTO mail_messages(id,account,payload,received_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,received_at=excluded.received_at`, message.ID, strings.ToLower(strings.TrimSpace(message.Account)), string(b), formatTime(message.ReceivedAt))
	return err
}

func (s *Store) MailMessages(query, mailType string) []model.MailMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT payload FROM mail_messages ORDER BY received_at DESC LIMIT 500")
	if err != nil {
		return []model.MailMessage{}
	}
	defer rows.Close()
	result := make([]model.MailMessage, 0)
	q := strings.ToLower(strings.TrimSpace(query))
	for rows.Next() {
		var raw string
		var m model.MailMessage
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &m) != nil {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(m.Account+" "+m.Subject+" "+m.Text+" "+m.Code), q) {
			continue
		}
		if mailType != "" && mailType != "all" && !strings.Contains(strings.ToLower(m.Subject+" "+m.Text), strings.ToLower(mailType)) {
			continue
		}
		result = append(result, m)
	}
	return result
}

func (s *Store) DeleteMailMessages(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if _, err := s.db.Exec("DELETE FROM mail_messages WHERE id=?", id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) MailAccounts() []model.MailAccountProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The mailbox's created_at is stored in the profile JSON.  Do not sort by
	// the table's updated_at column: credential refreshes and status changes
	// update that value and would make the list order jump around.
	rows, err := s.db.Query("SELECT profile, encrypted_credentials, updated_at FROM mail_accounts")
	if err != nil {
		return []model.MailAccountProfile{}
	}
	defer rows.Close()
	result := make([]model.MailAccountProfile, 0)
	for rows.Next() {
		var raw, encrypted, updatedAt string
		var p model.MailAccountProfile
		if rows.Scan(&raw, &encrypted, &updatedAt) == nil && json.Unmarshal([]byte(raw), &p) == nil {
			if p.CreatedAt.IsZero() {
				// Older records may predate the created_at field in the profile
				// JSON. Preserve a useful entry-time value for those records.
				p.CreatedAt, _ = parseTime(updatedAt)
			}
			if plain, e := s.decrypt(encrypted); e == nil {
				var c model.MailAccountCredentials
				if json.Unmarshal([]byte(plain), &c) == nil {
					p = mailProfileWithCredentials(p, c)
				}
			}
			result = append(result, p)
		}
	}
	// Newest imported/entered mail accounts are shown first.  Keep a stable
	// email tie-breaker so records with the same (or missing) timestamp remain
	// deterministic across requests.
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return strings.ToLower(result[i].Email) < strings.ToLower(result[j].Email)
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}

func (s *Store) MailAccountsByManagementScope(scope string) []model.MailAccountProfile {
	scope = normalizeMailManagementScope(scope)
	accounts := s.MailAccounts()
	result := make([]model.MailAccountProfile, 0, len(accounts))
	for _, account := range accounts {
		if normalizeMailManagementScope(account.ManagementScope) == scope {
			result = append(result, account)
		}
	}
	return result
}

func normalizeMailManagementScope(scope string) string {
	if strings.EqualFold(strings.TrimSpace(scope), "pro") {
		return "pro"
	}
	return "mail"
}

func mailProfileWithCredentials(p model.MailAccountProfile, c model.MailAccountCredentials) model.MailAccountProfile {
	c.MailPassword = cleanImportedCredential(c.MailPassword)
	c.ClientID = cleanImportedCredential(c.ClientID)
	c.MailRefreshToken = cleanImportedCredential(c.MailRefreshToken)
	c.PickupURL = cleanImportedCredential(c.PickupURL)
	c.GptPassword = cleanImportedCredential(c.GptPassword)
	c.TotpSecret = cleanImportedCredential(c.TotpSecret)
	c.AccessToken = cleanImportedCredential(c.AccessToken)
	c.RefreshToken = cleanImportedCredential(c.RefreshToken)
	c.IDToken = cleanImportedCredential(c.IDToken)
	c.ChatGPTSession = cleanImportedCredential(c.ChatGPTSession)
	p.MailPasswordPresent = strings.TrimSpace(c.MailPassword) != ""
	p.ClientIDPresent = strings.TrimSpace(c.ClientID) != ""
	p.MailRefreshPresent = strings.TrimSpace(c.MailRefreshToken) != ""
	p.PickupURLPresent = strings.TrimSpace(c.PickupURL) != ""
	p.GptPasswordPresent = strings.TrimSpace(c.GptPassword) != ""
	p.TotpSecretPresent = strings.TrimSpace(c.TotpSecret) != ""
	p.AccessTokenPresent = strings.TrimSpace(c.AccessToken) != ""
	p.RefreshTokenPresent = strings.TrimSpace(c.RefreshToken) != ""
	p.IDTokenPresent = strings.TrimSpace(c.IDToken) != ""
	p.ChatGPTSessionPresent = strings.TrimSpace(c.ChatGPTSession) != ""
	switch {
	case p.PickupURLPresent:
		p.LoginMethod = "directurl"
	case p.ClientIDPresent && p.MailRefreshPresent:
		p.LoginMethod = "outlook"
	case p.TotpSecretPresent:
		p.LoginMethod = "totp"
	default:
		p.LoginMethod = "mailtoken"
	}
	return p
}

func cleanImportedCredential(value string) string {
	v := strings.TrimSpace(value)
	if v == "<nil>" || v == "null" || v == "undefined" {
		return ""
	}
	return v
}

func (s *Store) SaveMailAccount(profile model.MailAccountProfile, credentials model.MailAccountCredentials) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile.Email = strings.ToLower(strings.TrimSpace(profile.Email))
	profile.Label = strings.TrimSpace(profile.Label)
	if profile.Email == "" || !strings.Contains(profile.Email, "@") {
		return model.MailAccountProfile{}, errors.New("邮箱地址无效")
	}
	now := time.Now()
	var oldProfile, encrypted string
	if profile.ID == "" {
		// Imports commonly omit the internal ID. Reuse an existing mailbox by
		// email so re-importing credentials does not reset its original entry
		// time or create a new identity.
		var existingID string
		err := s.db.QueryRow("SELECT id, profile, encrypted_credentials FROM mail_accounts WHERE email=?", profile.Email).Scan(&existingID, &oldProfile, &encrypted)
		switch {
		case err == nil:
			profile.ID = existingID
			var old model.MailAccountProfile
			_ = json.Unmarshal([]byte(oldProfile), &old)
			profile.CreatedAt = old.CreatedAt
			profile.ManagementScope, profile.ProManagedAt = old.ManagementScope, old.ProManagedAt
			profile.ChatGPTStatus, profile.ChatGPTStatusMessage, profile.ChatGPTStatusAt = old.ChatGPTStatus, old.ChatGPTStatusMessage, old.ChatGPTStatusAt
			preserveMailPlanCheck(&profile, old)
			preserveProProfile(&profile, old)
			if profile.RegistrationStatus == "" {
				profile.RegistrationStatus, profile.RegistrationLastError = old.RegistrationStatus, old.RegistrationLastError
			}
			if profile.CreatedAt.IsZero() {
				profile.CreatedAt = now
			}
		case errors.Is(err, sql.ErrNoRows):
			profile.ID = strconv.FormatInt(now.UnixNano(), 36)
			profile.CreatedAt = now
		default:
			return model.MailAccountProfile{}, err
		}
	} else if err := s.db.QueryRow("SELECT profile, encrypted_credentials FROM mail_accounts WHERE id=?", profile.ID).Scan(&oldProfile, &encrypted); err != nil {
		return model.MailAccountProfile{}, errors.New("邮箱账号不存在")
	} else {
		var old model.MailAccountProfile
		_ = json.Unmarshal([]byte(oldProfile), &old)
		profile.CreatedAt = old.CreatedAt
		profile.ManagementScope, profile.ProManagedAt = old.ManagementScope, old.ProManagedAt
		profile.ChatGPTStatus, profile.ChatGPTStatusMessage, profile.ChatGPTStatusAt = old.ChatGPTStatus, old.ChatGPTStatusMessage, old.ChatGPTStatusAt
		preserveMailPlanCheck(&profile, old)
		preserveProProfile(&profile, old)
		if profile.RegistrationStatus == "" {
			profile.RegistrationStatus, profile.RegistrationLastError = old.RegistrationStatus, old.RegistrationLastError
		}
		if profile.CreatedAt.IsZero() {
			profile.CreatedAt = now
		}
	}
	profile.UpdatedAt = now
	sealed, err := s.encrypt(mustJSON(credentials))
	if err != nil {
		return model.MailAccountProfile{}, err
	}
	encrypted = sealed
	profile.MailPasswordPresent = credentials.MailPassword != ""
	profile.ClientIDPresent = credentials.ClientID != ""
	profile.MailRefreshPresent = credentials.MailRefreshToken != ""
	profile.PickupURLPresent = credentials.PickupURL != ""
	profile.GptPasswordPresent = credentials.GptPassword != ""
	profile.TotpSecretPresent = credentials.TotpSecret != ""
	profile.AccessTokenPresent = credentials.AccessToken != ""
	profile.RefreshTokenPresent = credentials.RefreshToken != ""
	profile.IDTokenPresent = credentials.IDToken != ""
	profile.ChatGPTSessionPresent = credentials.ChatGPTSession != ""
	switch {
	case credentials.PickupURL != "":
		profile.LoginMethod = "directurl"
	case credentials.ClientID != "" && credentials.MailRefreshToken != "":
		profile.LoginMethod = "outlook"
	case credentials.TotpSecret != "":
		profile.LoginMethod = "totp"
	default:
		profile.LoginMethod = "mailtoken"
	}
	raw, _ := json.Marshal(profile)
	_, err = s.db.Exec(`INSERT INTO mail_accounts(id,email,label,group_name,profile,encrypted_credentials,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(email) DO UPDATE SET label=excluded.label,group_name=excluded.group_name,profile=excluded.profile,encrypted_credentials=excluded.encrypted_credentials,updated_at=excluded.updated_at`, profile.ID, profile.Email, profile.Label, profile.Group, string(raw), encrypted, formatTime(now))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return model.MailAccountProfile{}, errors.New("邮箱账号已存在")
		}
		return model.MailAccountProfile{}, err
	}
	return profile, nil
}

func preserveMailPlanCheck(profile *model.MailAccountProfile, old model.MailAccountProfile) {
	profile.CurrentPlanType = old.CurrentPlanType
	profile.SubscriptionPlan = old.SubscriptionPlan
	profile.HasActiveSubscription = old.HasActiveSubscription
	profile.PlusTrialEligible = old.PlusTrialEligible
	profile.PlanCheckStatus = old.PlanCheckStatus
	profile.PlanCheckedAt = old.PlanCheckedAt
	profile.PlanLastSuccessAt = old.PlanLastSuccessAt
	profile.PlanCheckHTTPStatus = old.PlanCheckHTTPStatus
	profile.PlanCheckError = old.PlanCheckError
	profile.PlanExpiresAt = old.PlanExpiresAt
	profile.PlanRenewsAt = old.PlanRenewsAt
	profile.BillingPeriod = old.BillingPeriod
	profile.BillingCurrency = old.BillingCurrency
}

func preserveProProfile(profile *model.MailAccountProfile, old model.MailAccountProfile) {
	profile.OAuthStatus, profile.OAuthAccountID, profile.OAuthUserID = old.OAuthStatus, old.OAuthAccountID, old.OAuthUserID
	profile.OAuthExpiresAt, profile.OAuthAuthorizedAt = old.OAuthExpiresAt, old.OAuthAuthorizedAt
	profile.PushProvider, profile.PushStatus = old.PushProvider, old.PushStatus
	profile.Sub2AccountID, profile.Sub2AccountName, profile.CPAAuthFileName = old.Sub2AccountID, old.Sub2AccountName, old.CPAAuthFileName
	profile.Quota5H, profile.Quota7D, profile.QuotaStatus, profile.QuotaCheckedAt = old.Quota5H, old.Quota7D, old.QuotaStatus, old.QuotaCheckedAt
	profile.SpaceMergedOnce, profile.SpaceMergedAt = old.SpaceMergedOnce, old.SpaceMergedAt
	profile.TargetAdminID, profile.TargetTeamID, profile.TargetSeatType = old.TargetAdminID, old.TargetTeamID, old.TargetSeatType
	profile.InviteStatus, profile.AcceptStatus, profile.TransferStatus, profile.RemoveStatus = old.InviteStatus, old.AcceptStatus, old.TransferStatus, old.RemoveStatus
	profile.ProLastError, profile.ProWorkflowRunning = old.ProLastError, old.ProWorkflowRunning
}

func (s *Store) UpdateMailAccountManagementScope(email, scope string) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	email = strings.ToLower(strings.TrimSpace(email))
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != "mail" && scope != "pro" {
		return model.MailAccountProfile{}, errors.New("账号归属只能是 mail 或 pro")
	}
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.MailAccountProfile{}, errors.New("邮箱账号不存在")
		}
		return model.MailAccountProfile{}, err
	}
	var profile model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return model.MailAccountProfile{}, err
	}
	now := time.Now()
	profile.ManagementScope, profile.UpdatedAt = scope, now
	if scope == "pro" {
		profile.ProManagedAt = &now
	} else {
		profile.ProManagedAt = nil
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		return model.MailAccountProfile{}, err
	}
	if _, err = s.db.Exec("UPDATE mail_accounts SET profile=?, updated_at=? WHERE email=?", string(encoded), formatTime(now), email); err != nil {
		return model.MailAccountProfile{}, err
	}
	return profile, nil
}

func (s *Store) MailAccountCredential(email string) (model.MailAccountProfile, model.MailAccountCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, encrypted string
	if err := s.db.QueryRow("SELECT profile, encrypted_credentials FROM mail_accounts WHERE email=?", strings.ToLower(strings.TrimSpace(email))).Scan(&raw, &encrypted); err != nil {
		return model.MailAccountProfile{}, model.MailAccountCredentials{}, errors.New("邮箱账号不存在")
	}
	var p model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, model.MailAccountCredentials{}, err
	}
	plain, err := s.decrypt(encrypted)
	if err != nil {
		return p, model.MailAccountCredentials{}, err
	}
	var c model.MailAccountCredentials
	if err := json.Unmarshal([]byte(plain), &c); err != nil {
		return p, c, errors.New("邮箱凭据格式无效")
	}
	c.Email = cleanImportedCredential(c.Email)
	c.MailPassword = cleanImportedCredential(c.MailPassword)
	c.ClientID = cleanImportedCredential(c.ClientID)
	c.MailRefreshToken = cleanImportedCredential(c.MailRefreshToken)
	c.PickupURL = cleanImportedCredential(c.PickupURL)
	c.GptPassword = cleanImportedCredential(c.GptPassword)
	c.TotpSecret = cleanImportedCredential(c.TotpSecret)
	c.AccessToken = cleanImportedCredential(c.AccessToken)
	c.RefreshToken = cleanImportedCredential(c.RefreshToken)
	c.IDToken = cleanImportedCredential(c.IDToken)
	c.ChatGPTSession = cleanImportedCredential(c.ChatGPTSession)
	p = mailProfileWithCredentials(p, c)
	return p, c, nil
}

// SaveMailAccountOAuth stores the ChatGPT/Codex OAuth credentials on the
// mailbox record itself.  Team OAuth is persisted in free_accounts, but the
// mail-management screen reads mail_accounts, so both projections must stay
// synchronized.
func (s *Store) SaveMailAccountOAuth(email, accessToken, refreshToken string) error {
	return s.SaveMailAccountOAuthBundle(email, accessToken, refreshToken, "", "", "", time.Time{})
}

// SaveMailAccountOAuthBundle atomically stores a Codex OAuth token bundle and
// its non-secret identity projection. A blank rotated RT keeps the old RT.
func (s *Store) SaveMailAccountOAuthBundle(email, accessToken, refreshToken, idToken, accountID, userID string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("邮箱地址无效")
	}
	var raw, encrypted string
	if err := s.db.QueryRow("SELECT profile, encrypted_credentials FROM mail_accounts WHERE email=?", email).Scan(&raw, &encrypted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("邮箱账号不存在")
		}
		return err
	}
	var profile model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return err
	}
	plain, err := s.decrypt(encrypted)
	if err != nil {
		return err
	}
	var credentials model.MailAccountCredentials
	if err := json.Unmarshal([]byte(plain), &credentials); err != nil {
		return errors.New("邮箱凭据格式无效")
	}
	credentials.Email = email
	if strings.TrimSpace(accessToken) != "" {
		credentials.AccessToken = strings.TrimSpace(accessToken)
	}
	if strings.TrimSpace(refreshToken) != "" {
		credentials.RefreshToken = strings.TrimSpace(refreshToken)
	}
	if strings.TrimSpace(idToken) != "" {
		credentials.IDToken = strings.TrimSpace(idToken)
	}
	profile = mailProfileWithCredentials(profile, credentials)
	now := time.Now()
	profile.OAuthStatus, profile.OAuthAuthorizedAt = "completed", &now
	if strings.TrimSpace(accountID) != "" {
		profile.OAuthAccountID = strings.TrimSpace(accountID)
	}
	if strings.TrimSpace(userID) != "" {
		profile.OAuthUserID = strings.TrimSpace(userID)
	}
	if !expiresAt.IsZero() {
		profile.OAuthExpiresAt = &expiresAt
	}
	profile.ProLastError, profile.UpdatedAt = "", now
	sealed, err := s.encrypt(mustJSON(credentials))
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE mail_accounts SET profile=?, encrypted_credentials=?, updated_at=? WHERE email=?", string(encoded), sealed, formatTime(profile.UpdatedAt), email)
	return err
}

func (s *Store) UpdateMailAccountStatus(email, status, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", strings.ToLower(strings.TrimSpace(email))).Scan(&raw); err != nil {
		return err
	}
	var p model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	p.RegistrationStatus, p.RegistrationLastError, p.UpdatedAt = status, message, time.Now()
	encoded, _ := json.Marshal(p)
	_, err := s.db.Exec("UPDATE mail_accounts SET profile=?, updated_at=? WHERE email=?", string(encoded), formatTime(p.UpdatedAt), p.Email)
	return err
}

// MarkMailAccountDead records a confirmed OpenAI account deactivation on the
// mail-management projection.  Keep both the dedicated ChatGPT status and the
// legacy registration status in sync so all existing views show the same state.
func (s *Store) MarkMailAccountDead(email, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	email = strings.ToLower(strings.TrimSpace(email))
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", email).Scan(&raw); err != nil {
		return err
	}
	var p model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return err
	}
	now := time.Now()
	p.ChatGPTStatus, p.ChatGPTStatusMessage, p.ChatGPTStatusAt = "dead", strings.TrimSpace(message), &now
	p.RegistrationStatus, p.RegistrationLastError, p.UpdatedAt = "dead", strings.TrimSpace(message), now
	encoded, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE mail_accounts SET profile=?, updated_at=? WHERE email=?", string(encoded), formatTime(now), email)
	return err
}

func (s *Store) UpdateMailAccountATStatus(email string, checkedAt time.Time, valid bool, httpStatus int, message string) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", strings.ToLower(strings.TrimSpace(email))).Scan(&raw); err != nil {
		return model.MailAccountProfile{}, err
	}
	var p model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return model.MailAccountProfile{}, err
	}
	p.ATCheckedAt = &checkedAt
	p.ATValid = valid
	p.ATCheckHTTPStatus = httpStatus
	p.ATCheckMessage = message
	p.UpdatedAt = time.Now()
	encoded, err := json.Marshal(p)
	if err != nil {
		return model.MailAccountProfile{}, err
	}
	if _, err = s.db.Exec("UPDATE mail_accounts SET profile=?, updated_at=? WHERE email=?", string(encoded), formatTime(p.UpdatedAt), p.Email); err != nil {
		return model.MailAccountProfile{}, err
	}
	return p, nil
}

func (s *Store) UpdateMailAccountPlanCheck(email string, result model.AccountPlanCheckResult) (model.MailAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM mail_accounts WHERE email=?", strings.ToLower(strings.TrimSpace(email))).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.MailAccountProfile{}, errors.New("邮箱账号不存在")
		}
		return model.MailAccountProfile{}, err
	}
	var profile model.MailAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return model.MailAccountProfile{}, err
	}
	checkedAt := result.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	profile.PlanCheckedAt = &checkedAt
	profile.PlanCheckHTTPStatus = result.HTTPStatus
	profile.UpdatedAt = time.Now()
	if result.OK {
		profile.CurrentPlanType = strings.TrimSpace(result.CurrentPlanType)
		profile.SubscriptionPlan = strings.TrimSpace(result.SubscriptionPlan)
		profile.HasActiveSubscription = result.HasActiveSubscription
		profile.PlusTrialEligible = result.PlusTrialEligible
		profile.PlanExpiresAt = strings.TrimSpace(result.ExpiresAt)
		profile.PlanRenewsAt = strings.TrimSpace(result.RenewsAt)
		profile.BillingPeriod = strings.TrimSpace(result.BillingPeriod)
		profile.BillingCurrency = strings.TrimSpace(result.BillingCurrency)
		profile.PlanCheckStatus, profile.PlanCheckError = "success", ""
		profile.PlanLastSuccessAt = &checkedAt
	} else {
		// Keep the last successful plan visible while surfacing this attempt's
		// error, matching turb's account-list behavior.
		profile.PlanCheckStatus = "failed"
		profile.PlanCheckError = strings.TrimSpace(result.Error)
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		return model.MailAccountProfile{}, err
	}
	if _, err = s.db.Exec("UPDATE mail_accounts SET profile=?, updated_at=? WHERE email=?", string(encoded), formatTime(profile.UpdatedAt), profile.Email); err != nil {
		return model.MailAccountProfile{}, err
	}
	return profile, nil
}

func (s *Store) DeleteMailAccount(email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec("DELETE FROM mail_accounts WHERE email=?", strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return errors.New("邮箱账号不存在")
	}
	return nil
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func (s *Store) ensureOpenAIRefreshColumn() error {
	rows, err := s.db.Query("PRAGMA table_info(openai_accounts)")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "encrypted_refresh_token" {
			found = true
			break
		}
	}
	if !found {
		_, err = s.db.Exec("ALTER TABLE openai_accounts ADD COLUMN encrypted_refresh_token TEXT NOT NULL DEFAULT ''")
	}
	return err
}

func (s *Store) initialize(dataDir string) error {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM settings").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		settings := s.Settings()
		return s.SaveSettings(settings)
	}
	legacyPath := filepath.Join(dataDir, "state.json")
	data, err := os.ReadFile(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return s.SaveSettings(model.DefaultSettings())
	}
	if err != nil {
		return err
	}
	legacy := diskState{Settings: model.DefaultSettings()}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("读取旧 state.json 失败: %w", err)
	}
	if legacy.Settings.BaseURL == "" {
		legacy.Settings = model.DefaultSettings()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	settingsJSON, _ := json.Marshal(legacy.Settings)
	if _, err := tx.Exec("INSERT INTO settings(id, payload) VALUES(1, ?)", string(settingsJSON)); err != nil {
		return err
	}
	for _, profile := range legacy.Proxies {
		if _, err := tx.Exec("INSERT OR REPLACE INTO proxies(id, name, url, created_at, updated_at) VALUES(?, ?, ?, ?, ?)", profile.ID, profile.Name, profile.URL, formatTime(profile.CreatedAt), formatTime(profile.UpdatedAt)); err != nil {
			return err
		}
	}
	for _, account := range legacy.Accounts {
		profileJSON, _ := json.Marshal(account.Profile)
		if _, err := tx.Exec("INSERT OR REPLACE INTO admin_accounts(id, label, profile, encrypted_access_token, encrypted_refresh_token) VALUES(?, ?, ?, ?, ?)", account.Profile.ID, account.Profile.Label, string(profileJSON), account.EncryptedToken, account.EncryptedRefreshToken); err != nil {
			return err
		}
	}
	for _, entry := range legacy.History {
		if entry.Operation == "" {
			entry.Operation = "full"
		}
		payload, _ := json.Marshal(entry)
		if _, err := tx.Exec("INSERT OR REPLACE INTO history(id, completed_at, payload) VALUES(?, ?, ?)", entry.ID, formatTime(entry.CompletedAt), string(payload)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = os.Rename(legacyPath, legacyPath+".migrated.bak")
	return nil
}

func loadMasterKey(dataDir string) ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv("APP_MASTER_KEY")); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(key) != 32 {
			return nil, errors.New("APP_MASTER_KEY 必须是 Base64 编码的 32 字节密钥")
		}
		return key, nil
	}
	path := filepath.Join(dataDir, ".master-key")
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("本地主密钥长度无效")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, err
	}
	return key, nil
}

func (s *Store) AdminAccounts() []model.AdminAccountProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT profile FROM admin_accounts ORDER BY label COLLATE NOCASE")
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make([]model.AdminAccountProfile, 0)
	for rows.Next() {
		var raw string
		var profile model.AdminAccountProfile
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &profile) == nil {
			result = append(result, profile)
		}
	}
	_ = rows.Close()
	s.reconcileAdminTeamRotationChildCounts(result)
	return result
}

// reconcileAdminTeamRotationChildCounts backfills counters for older data that
// predates the persisted counter. It only raises a counter, so repeated reads
// cannot erase additional re-entry counts recorded by the workflow.
func (s *Store) reconcileAdminTeamRotationChildCounts(accounts []model.AdminAccountProfile) {
	if len(accounts) == 0 {
		return
	}
	counts := make(map[string]int, len(accounts))
	costs := make(map[string]float64, len(accounts))
	rows, err := s.db.Query("SELECT profile FROM free_accounts")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var profile model.FreeAccountProfile
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &profile) != nil {
			continue
		}
		if profile.AdminAccountID != "" && profile.AcceptStatus == "completed" {
			counts[profile.AdminAccountID]++
		}
		if len(profile.CostByAdmin) > 0 {
			for adminID, cost := range profile.CostByAdmin {
				if adminID != "" && cost > 0 {
					costs[adminID] += cost
				}
			}
		} else if profile.AdminAccountID != "" && profile.TotalCostUSD > 0 {
			// Compatibility for an account written between the introduction of
			// total_cost_usd and per-Team cost attribution.
			costs[profile.AdminAccountID] += profile.TotalCostUSD
		}
	}
	for i := range accounts {
		accounts[i].TeamRotationChildCost = costs[accounts[i].ID]
		count := counts[accounts[i].ID]
		if count <= accounts[i].TeamRotationChildCount {
			continue
		}
		accounts[i].TeamRotationChildCount = count
		accounts[i].UpdatedAt = time.Now()
		encoded, marshalErr := json.Marshal(accounts[i])
		if marshalErr == nil {
			_, _ = s.db.Exec("UPDATE admin_accounts SET profile=? WHERE id=?", string(encoded), accounts[i].ID)
		}
	}
}

func (s *Store) SaveAdminAccount(profile model.AdminAccountProfile, token string) (model.AdminAccountProfile, error) {
	return s.SaveAdminAccountCredentials(profile, token, "")
}

func (s *Store) SaveAdminAccountCredentials(profile model.AdminAccountProfile, accessToken, refreshToken string) (model.AdminAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile.Label = strings.TrimSpace(profile.Label)
	now := time.Now()
	var encrypted, encryptedRefresh string
	if profile.ID == "" {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM admin_accounts").Scan(&count); err != nil {
			return model.AdminAccountProfile{}, err
		}
		if count >= 50 {
			return model.AdminAccountProfile{}, errors.New("最多保存 50 个母号")
		}
		if accessToken == "" {
			return model.AdminAccountProfile{}, errors.New("Access Token 不能为空")
		}
		var err error
		encrypted, err = s.encrypt(accessToken)
		if err != nil {
			return model.AdminAccountProfile{}, err
		}
		if refreshToken != "" {
			encryptedRefresh, err = s.encrypt(refreshToken)
			if err != nil {
				return model.AdminAccountProfile{}, err
			}
		}
		profile.ID, profile.CreatedAt, profile.UpdatedAt = strconv.FormatInt(now.UnixNano(), 36), now, now
	} else {
		var raw string
		if err := s.db.QueryRow("SELECT profile, encrypted_access_token, encrypted_refresh_token FROM admin_accounts WHERE id = ?", profile.ID).Scan(&raw, &encrypted, &encryptedRefresh); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return model.AdminAccountProfile{}, errors.New("母号配置不存在")
			}
			return model.AdminAccountProfile{}, err
		}
		var existing model.AdminAccountProfile
		if err := json.Unmarshal([]byte(raw), &existing); err != nil {
			return model.AdminAccountProfile{}, err
		}
		profile.CreatedAt, profile.UpdatedAt = existing.CreatedAt, now
		profile.TeamRotationChildCount = existing.TeamRotationChildCount
		if accessToken != "" {
			var err error
			encrypted, err = s.encrypt(accessToken)
			if err != nil {
				return model.AdminAccountProfile{}, err
			}
		}
		if refreshToken != "" {
			var err error
			encryptedRefresh, err = s.encrypt(refreshToken)
			if err != nil {
				return model.AdminAccountProfile{}, err
			}
		}
	}
	profile.TokenPresent, profile.RefreshTokenPresent = encrypted != "", encryptedRefresh != ""
	raw, err := json.Marshal(profile)
	if err != nil {
		return model.AdminAccountProfile{}, err
	}
	_, err = s.db.Exec(`INSERT INTO admin_accounts(id, label, profile, encrypted_access_token, encrypted_refresh_token)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET label=excluded.label, profile=excluded.profile,
		encrypted_access_token=excluded.encrypted_access_token, encrypted_refresh_token=excluded.encrypted_refresh_token`,
		profile.ID, profile.Label, string(raw), encrypted, encryptedRefresh)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return model.AdminAccountProfile{}, errors.New("母号名称已存在")
		}
		return model.AdminAccountProfile{}, err
	}
	return profile, nil
}

// IncrementAdminTeamRotationChildCount records one successful child-account
// entry into a Team rotation. The update is serialized with all other store
// writes so retries cannot corrupt the cumulative counter.
func (s *Store) IncrementAdminTeamRotationChildCount(id string) (model.AdminAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return model.AdminAccountProfile{}, errors.New("母号 ID 不能为空")
	}
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM admin_accounts WHERE id = ?", id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.AdminAccountProfile{}, errors.New("母号配置不存在")
		}
		return model.AdminAccountProfile{}, err
	}
	var profile model.AdminAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return model.AdminAccountProfile{}, err
	}
	profile.TeamRotationChildCount++
	profile.UpdatedAt = time.Now()
	encoded, err := json.Marshal(profile)
	if err != nil {
		return model.AdminAccountProfile{}, err
	}
	if _, err = s.db.Exec("UPDATE admin_accounts SET profile=? WHERE id=?", string(encoded), id); err != nil {
		return model.AdminAccountProfile{}, err
	}
	return profile, nil
}

func (s *Store) AdminAccountToken(id string) (model.AdminAccountProfile, string, error) {
	profile, credentials, err := s.AdminAccountCredential(id)
	return profile, credentials.AccessToken, err
}

func (s *Store) AdminAccountCredential(id string) (model.AdminAccountProfile, AdminAccountCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, encrypted, encryptedRefresh string
	if err := s.db.QueryRow("SELECT profile, encrypted_access_token, encrypted_refresh_token FROM admin_accounts WHERE id = ?", id).Scan(&raw, &encrypted, &encryptedRefresh); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.AdminAccountProfile{}, AdminAccountCredentials{}, errors.New("母号配置不存在")
		}
		return model.AdminAccountProfile{}, AdminAccountCredentials{}, err
	}
	var profile model.AdminAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return model.AdminAccountProfile{}, AdminAccountCredentials{}, err
	}
	accessToken, err := s.decrypt(encrypted)
	if err != nil {
		return model.AdminAccountProfile{}, AdminAccountCredentials{}, err
	}
	refreshToken := ""
	if encryptedRefresh != "" {
		refreshToken, err = s.decrypt(encryptedRefresh)
		if err != nil {
			return model.AdminAccountProfile{}, AdminAccountCredentials{}, err
		}
	}
	return profile, AdminAccountCredentials{AccessToken: accessToken, RefreshToken: refreshToken}, nil
}

func (s *Store) DeleteAdminAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec("DELETE FROM admin_accounts WHERE id = ?", id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return errors.New("母号配置不存在")
	}
	return nil
}

func (s *Store) OpenAIAccounts() []model.OpenAIAccountProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT profile FROM openai_accounts ORDER BY label COLLATE NOCASE")
	if err != nil {
		return []model.OpenAIAccountProfile{}
	}
	defer rows.Close()
	result := make([]model.OpenAIAccountProfile, 0)
	for rows.Next() {
		var raw string
		var profile model.OpenAIAccountProfile
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &profile) == nil {
			compactOpenAIStatus(&profile)
			result = append(result, profile)
		}
	}
	return result
}

func (s *Store) SaveOpenAIAccount(profile model.OpenAIAccountProfile, accessToken string, refreshTokens ...string) (model.OpenAIAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile.Label = strings.TrimSpace(profile.Label)
	accessToken = strings.TrimSpace(accessToken)
	refreshToken := ""
	if len(refreshTokens) > 0 {
		refreshToken = strings.TrimSpace(refreshTokens[0])
	}
	now := time.Now()
	var encrypted, encryptedRefresh string
	if profile.ID == "" {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM openai_accounts").Scan(&count); err != nil {
			return model.OpenAIAccountProfile{}, err
		}
		if count >= 500 {
			return model.OpenAIAccountProfile{}, errors.New("最多保存 500 个 OpenAI 账号")
		}
		if accessToken == "" {
			return model.OpenAIAccountProfile{}, errors.New("Access Token 不能为空")
		}
		var err error
		encrypted, err = s.encrypt(accessToken)
		if err != nil {
			return model.OpenAIAccountProfile{}, err
		}
		if refreshToken != "" {
			encryptedRefresh, err = s.encrypt(refreshToken)
			if err != nil {
				return model.OpenAIAccountProfile{}, err
			}
		}
		profile.ID, profile.CreatedAt, profile.UpdatedAt = strconv.FormatInt(now.UnixNano(), 36), now, now
	} else {
		var raw string
		if err := s.db.QueryRow("SELECT profile, encrypted_access_token, encrypted_refresh_token FROM openai_accounts WHERE id = ?", profile.ID).Scan(&raw, &encrypted, &encryptedRefresh); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return model.OpenAIAccountProfile{}, errors.New("OpenAI 账号不存在")
			}
			return model.OpenAIAccountProfile{}, err
		}
		var existing model.OpenAIAccountProfile
		if err := json.Unmarshal([]byte(raw), &existing); err != nil {
			return model.OpenAIAccountProfile{}, err
		}
		profile.CreatedAt, profile.UpdatedAt = existing.CreatedAt, now
		if accessToken != "" {
			var err error
			encrypted, err = s.encrypt(accessToken)
			if err != nil {
				return model.OpenAIAccountProfile{}, err
			}
		}
		if refreshToken != "" {
			var err error
			encryptedRefresh, err = s.encrypt(refreshToken)
			if err != nil {
				return model.OpenAIAccountProfile{}, err
			}
		}
	}
	profile.TokenPresent = encrypted != ""
	profile.RefreshTokenPresent = encryptedRefresh != ""
	raw, err := json.Marshal(profile)
	if err != nil {
		return model.OpenAIAccountProfile{}, err
	}
	_, err = s.db.Exec(`INSERT INTO openai_accounts(id, label, profile, encrypted_access_token, encrypted_refresh_token)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET label=excluded.label, profile=excluded.profile,
		encrypted_access_token=excluded.encrypted_access_token, encrypted_refresh_token=excluded.encrypted_refresh_token`, profile.ID, profile.Label, string(raw), encrypted, encryptedRefresh)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return model.OpenAIAccountProfile{}, errors.New("OpenAI 账号名称已存在")
		}
		return model.OpenAIAccountProfile{}, err
	}
	return profile, nil
}

func (s *Store) OpenAIAccountCredential(id string) (model.OpenAIAccountProfile, model.OpenAIAccountCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw, encrypted, encryptedRefresh string
	if err := s.db.QueryRow("SELECT profile, encrypted_access_token, encrypted_refresh_token FROM openai_accounts WHERE id = ?", id).Scan(&raw, &encrypted, &encryptedRefresh); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.OpenAIAccountProfile{}, model.OpenAIAccountCredentials{}, errors.New("OpenAI 账号不存在")
		}
		return model.OpenAIAccountProfile{}, model.OpenAIAccountCredentials{}, err
	}
	var profile model.OpenAIAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return model.OpenAIAccountProfile{}, model.OpenAIAccountCredentials{}, err
	}
	compactOpenAIStatus(&profile)
	token, err := s.decrypt(encrypted)
	if err != nil {
		return model.OpenAIAccountProfile{}, model.OpenAIAccountCredentials{}, err
	}
	refreshToken := ""
	if encryptedRefresh != "" {
		refreshToken, err = s.decrypt(encryptedRefresh)
		if err != nil {
			return model.OpenAIAccountProfile{}, model.OpenAIAccountCredentials{}, err
		}
	}
	return profile, model.OpenAIAccountCredentials{AccessToken: token, RefreshToken: refreshToken}, nil
}

func (s *Store) DeleteOpenAIAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec("DELETE FROM openai_accounts WHERE id = ?", id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return errors.New("OpenAI 账号不存在")
	}
	return nil
}

// UpdateOpenAIAccountStatus updates only non-secret runtime fields after a
// validity or quota probe. The encrypted token is never returned or rewritten.
func (s *Store) UpdateOpenAIAccountStatus(id string, mutate func(*model.OpenAIAccountProfile)) (model.OpenAIAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err := s.db.QueryRow("SELECT profile FROM openai_accounts WHERE id = ?", id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.OpenAIAccountProfile{}, errors.New("OpenAI 账号不存在")
		}
		return model.OpenAIAccountProfile{}, err
	}
	var profile model.OpenAIAccountProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return model.OpenAIAccountProfile{}, err
	}
	if mutate != nil {
		mutate(&profile)
	}
	compactOpenAIStatus(&profile)
	profile.UpdatedAt = time.Now()
	encoded, err := json.Marshal(profile)
	if err != nil {
		return model.OpenAIAccountProfile{}, err
	}
	if _, err := s.db.Exec("UPDATE openai_accounts SET profile=? WHERE id=?", string(encoded), id); err != nil {
		return model.OpenAIAccountProfile{}, err
	}
	return profile, nil
}

func compactOpenAIStatus(profile *model.OpenAIAccountProfile) {
	if profile == nil || profile.LastCheckedAt == nil {
		return
	}
	if profile.LastCheckValid {
		profile.LastCheckMessage = "AT 有效"
	} else {
		profile.LastCheckMessage = "AT 无效"
	}
}

func (s *Store) encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *Store) decrypt(encoded string) (string, error) {
	data, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", errors.New("母号凭据密文无效")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("母号凭据密文无效")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("母号凭据解密失败，请检查 APP_MASTER_KEY")
	}
	return string(plain), nil
}

func (s *Store) Settings() model.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := model.DefaultSettings()
	var raw string
	if err := s.db.QueryRow("SELECT payload FROM settings WHERE id = 1").Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &settings)
	}
	return settings
}

func (s *Store) SaveSettings(settings model.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO settings(id, payload) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", string(raw))
	return err
}

func (s *Store) Proxies() []model.ProxyProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT id, name, url, created_at, updated_at FROM proxies ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make([]model.ProxyProfile, 0)
	for rows.Next() {
		var item model.ProxyProfile
		var created, updated string
		if rows.Scan(&item.ID, &item.Name, &item.URL, &created, &updated) == nil {
			item.CreatedAt, _ = parseTime(created)
			item.UpdatedAt, _ = parseTime(updated)
			result = append(result, item)
		}
	}
	return result
}

func (s *Store) HasProxyURL(proxyURL string) bool {
	if strings.TrimSpace(proxyURL) == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM proxies WHERE url = ?", proxyURL).Scan(&count)
	return count > 0
}

func (s *Store) SaveProxy(profile model.ProxyProfile) (model.ProxyProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile.Name, profile.URL = strings.TrimSpace(profile.Name), strings.TrimSpace(profile.URL)
	now := time.Now()
	if profile.ID == "" {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM proxies").Scan(&count); err != nil {
			return model.ProxyProfile{}, err
		}
		if count >= 50 {
			return model.ProxyProfile{}, errors.New("最多保存 50 个代理")
		}
		profile.ID, profile.CreatedAt, profile.UpdatedAt = strconv.FormatInt(now.UnixNano(), 36), now, now
		_, err := s.db.Exec("INSERT INTO proxies(id, name, url, created_at, updated_at) VALUES(?, ?, ?, ?, ?)", profile.ID, profile.Name, profile.URL, formatTime(profile.CreatedAt), formatTime(profile.UpdatedAt))
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return model.ProxyProfile{}, errors.New("代理名称已存在")
			}
			return model.ProxyProfile{}, err
		}
		return profile, nil
	}
	var oldURL, created string
	if err := s.db.QueryRow("SELECT url, created_at FROM proxies WHERE id = ?", profile.ID).Scan(&oldURL, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ProxyProfile{}, errors.New("代理配置不存在")
		}
		return model.ProxyProfile{}, err
	}
	profile.CreatedAt, _ = parseTime(created)
	profile.UpdatedAt = now
	tx, err := s.db.Begin()
	if err != nil {
		return model.ProxyProfile{}, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE proxies SET name=?, url=?, updated_at=? WHERE id=?", profile.Name, profile.URL, formatTime(now), profile.ID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return model.ProxyProfile{}, errors.New("代理名称已存在")
		}
		return model.ProxyProfile{}, err
	}
	settings := model.DefaultSettings()
	var raw string
	if tx.QueryRow("SELECT payload FROM settings WHERE id=1").Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &settings)
		if settings.ProxyURL == oldURL {
			settings.ProxyURL = profile.URL
			updated, _ := json.Marshal(settings)
			if _, err = tx.Exec("UPDATE settings SET payload=? WHERE id=1", string(updated)); err != nil {
				return model.ProxyProfile{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return model.ProxyProfile{}, err
	}
	return profile, nil
}

func (s *Store) DeleteProxy(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var proxyURL string
	if err := s.db.QueryRow("SELECT url FROM proxies WHERE id=?", id).Scan(&proxyURL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("代理配置不存在")
		}
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM proxies WHERE id=?", id); err != nil {
		return err
	}
	settings := model.DefaultSettings()
	var raw string
	if tx.QueryRow("SELECT payload FROM settings WHERE id=1").Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &settings)
		if settings.ProxyURL == proxyURL {
			settings.ProxyURL = ""
			updated, _ := json.Marshal(settings)
			if _, err = tx.Exec("UPDATE settings SET payload=? WHERE id=1", string(updated)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) AddHistory(entry model.HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.Operation == "" {
		entry.Operation = "full"
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT OR REPLACE INTO history(id, completed_at, payload) VALUES(?, ?, ?)", entry.ID, formatTime(entry.CompletedAt), string(payload)); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM history WHERE id NOT IN (SELECT id FROM history ORDER BY completed_at DESC LIMIT 100)"); err != nil {
		return err
	}
	for _, result := range entry.Results {
		if result.UserID == "" {
			continue
		}
		progress := model.AccountProgress{TeamAccountID: entry.TeamAccountID, AdminEmail: entry.AdminEmail, UserID: result.UserID, Email: result.Email}
		var existingRaw string
		if scanErr := tx.QueryRow("SELECT payload FROM account_progress WHERE team_account_id=? AND user_id=?", entry.TeamAccountID, result.UserID).Scan(&existingRaw); scanErr == nil {
			_ = json.Unmarshal([]byte(existingRaw), &progress)
		}
		progress.AdminEmail, progress.Email = entry.AdminEmail, result.Email
		progress.LastOperation, progress.LastStatus, progress.LastError = entry.Operation, result.Status, result.Error
		progress.UpdatedAt = entry.CompletedAt
		for _, step := range result.CompletedSteps {
			switch step {
			case "accept":
				at := entry.CompletedAt
				progress.EnteredAt, progress.RemovedAt = &at, nil
			case "transfer":
				at := entry.CompletedAt
				progress.TransferredAt = &at
			case "kick":
				at := entry.CompletedAt
				progress.RemovedAt = &at
			}
		}
		progressRaw, _ := json.Marshal(progress)
		if _, err = tx.Exec(`INSERT INTO account_progress(team_account_id, user_id, payload, updated_at) VALUES(?, ?, ?, ?)
			ON CONFLICT(team_account_id, user_id) DO UPDATE SET payload=excluded.payload, updated_at=excluded.updated_at`, entry.TeamAccountID, result.UserID, string(progressRaw), formatTime(progress.UpdatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) History() []model.HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT payload FROM history ORDER BY completed_at DESC LIMIT 100")
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make([]model.HistoryEntry, 0)
	for rows.Next() {
		var raw string
		var entry model.HistoryEntry
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &entry) == nil {
			if entry.Operation == "" {
				entry.Operation = "full"
			}
			result = append(result, entry)
		}
	}
	return result
}

func (s *Store) AccountProgress(teamAccountID string) []model.AccountProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	query, args := "SELECT payload FROM account_progress", []any{}
	if strings.TrimSpace(teamAccountID) != "" {
		query, args = query+" WHERE team_account_id=?", []any{strings.TrimSpace(teamAccountID)}
	}
	rows, err := s.db.Query(query+" ORDER BY updated_at DESC", args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make([]model.AccountProgress, 0)
	for rows.Next() {
		var raw string
		var item model.AccountProgress
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &item) == nil {
			result = append(result, item)
		}
	}
	return result
}

func (s *Store) ClearHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM history")
	return err
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
