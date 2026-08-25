package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgpt-space-merge/internal/model"
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
	mu    sync.Mutex
	path  string
	key   []byte
	state diskState
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	key, err := loadMasterKey(dataDir)
	if err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dataDir, "state.json"), key: key, state: diskState{Settings: model.DefaultSettings()}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, s.saveLocked()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, err
	}
	if s.state.Settings.BaseURL == "" {
		s.state.Settings = model.DefaultSettings()
	}
	return s, nil
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
	result := make([]model.AdminAccountProfile, 0, len(s.state.Accounts))
	for _, account := range s.state.Accounts {
		result = append(result, account.Profile)
	}
	return result
}

func (s *Store) SaveAdminAccount(profile model.AdminAccountProfile, token string) (model.AdminAccountProfile, error) {
	return s.SaveAdminAccountCredentials(profile, token, "")
}

func (s *Store) SaveAdminAccountCredentials(profile model.AdminAccountProfile, accessToken, refreshToken string) (model.AdminAccountProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile.Label = strings.TrimSpace(profile.Label)
	for _, existing := range s.state.Accounts {
		if strings.EqualFold(existing.Profile.Label, profile.Label) && existing.Profile.ID != profile.ID {
			return model.AdminAccountProfile{}, errors.New("母号名称已存在")
		}
	}
	now := time.Now()
	if profile.ID == "" {
		if len(s.state.Accounts) >= 50 {
			return model.AdminAccountProfile{}, errors.New("最多保存 50 个母号")
		}
		if accessToken == "" {
			return model.AdminAccountProfile{}, errors.New("Access Token 不能为空")
		}
		encrypted, err := s.encrypt(accessToken)
		if err != nil {
			return model.AdminAccountProfile{}, err
		}
		encryptedRefresh := ""
		if refreshToken != "" {
			encryptedRefresh, err = s.encrypt(refreshToken)
			if err != nil {
				return model.AdminAccountProfile{}, err
			}
		}
		profile.ID, profile.CreatedAt, profile.UpdatedAt = strconv.FormatInt(now.UnixNano(), 36), now, now
		profile.TokenPresent, profile.RefreshTokenPresent = true, encryptedRefresh != ""
		s.state.Accounts = append(s.state.Accounts, storedAdminAccount{Profile: profile, EncryptedToken: encrypted, EncryptedRefreshToken: encryptedRefresh})
	} else {
		found := false
		for index := range s.state.Accounts {
			if s.state.Accounts[index].Profile.ID != profile.ID {
				continue
			}
			profile.CreatedAt, profile.UpdatedAt, profile.TokenPresent = s.state.Accounts[index].Profile.CreatedAt, now, true
			encrypted := s.state.Accounts[index].EncryptedToken
			encryptedRefresh := s.state.Accounts[index].EncryptedRefreshToken
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
			profile.TokenPresent, profile.RefreshTokenPresent = encrypted != "", encryptedRefresh != ""
			s.state.Accounts[index] = storedAdminAccount{Profile: profile, EncryptedToken: encrypted, EncryptedRefreshToken: encryptedRefresh}
			found = true
			break
		}
		if !found {
			return model.AdminAccountProfile{}, errors.New("母号配置不存在")
		}
	}
	if err := s.saveLocked(); err != nil {
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
	for _, account := range s.state.Accounts {
		if account.Profile.ID != id {
			continue
		}
		accessToken, err := s.decrypt(account.EncryptedToken)
		if err != nil {
			return model.AdminAccountProfile{}, AdminAccountCredentials{}, err
		}
		refreshToken := ""
		if account.EncryptedRefreshToken != "" {
			refreshToken, err = s.decrypt(account.EncryptedRefreshToken)
			if err != nil {
				return model.AdminAccountProfile{}, AdminAccountCredentials{}, err
			}
		}
		return account.Profile, AdminAccountCredentials{AccessToken: accessToken, RefreshToken: refreshToken}, nil
	}
	return model.AdminAccountProfile{}, AdminAccountCredentials{}, errors.New("母号配置不存在")
}

func (s *Store) DeleteAdminAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, account := range s.state.Accounts {
		if account.Profile.ID != id {
			continue
		}
		s.state.Accounts = append(s.state.Accounts[:index], s.state.Accounts[index+1:]...)
		return s.saveLocked()
	}
	return errors.New("母号配置不存在")
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
	return s.state.Settings
}

func (s *Store) SaveSettings(settings model.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Settings = settings
	return s.saveLocked()
}

func (s *Store) Proxies() []model.ProxyProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]model.ProxyProfile, len(s.state.Proxies))
	copy(result, s.state.Proxies)
	return result
}

func (s *Store) HasProxyURL(proxyURL string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(proxyURL) == "" {
		return true
	}
	for _, profile := range s.state.Proxies {
		if profile.URL == proxyURL {
			return true
		}
	}
	return false
}

func (s *Store) SaveProxy(profile model.ProxyProfile) (model.ProxyProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile.Name = strings.TrimSpace(profile.Name)
	profile.URL = strings.TrimSpace(profile.URL)
	for _, existing := range s.state.Proxies {
		if strings.EqualFold(existing.Name, profile.Name) && existing.ID != profile.ID {
			return model.ProxyProfile{}, errors.New("代理名称已存在")
		}
	}
	now := time.Now()
	if profile.ID == "" {
		if len(s.state.Proxies) >= 50 {
			return model.ProxyProfile{}, errors.New("最多保存 50 个代理")
		}
		profile.ID = strconv.FormatInt(now.UnixNano(), 36)
		profile.CreatedAt, profile.UpdatedAt = now, now
		s.state.Proxies = append(s.state.Proxies, profile)
	} else {
		found := false
		for index := range s.state.Proxies {
			if s.state.Proxies[index].ID != profile.ID {
				continue
			}
			oldURL := s.state.Proxies[index].URL
			profile.CreatedAt = s.state.Proxies[index].CreatedAt
			profile.UpdatedAt = now
			s.state.Proxies[index] = profile
			if s.state.Settings.ProxyURL == oldURL {
				s.state.Settings.ProxyURL = profile.URL
			}
			found = true
			break
		}
		if !found {
			return model.ProxyProfile{}, fmt.Errorf("代理配置不存在")
		}
	}
	if err := s.saveLocked(); err != nil {
		return model.ProxyProfile{}, err
	}
	return profile, nil
}

func (s *Store) DeleteProxy(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, profile := range s.state.Proxies {
		if profile.ID != id {
			continue
		}
		if s.state.Settings.ProxyURL == profile.URL {
			s.state.Settings.ProxyURL = ""
		}
		s.state.Proxies = append(s.state.Proxies[:index], s.state.Proxies[index+1:]...)
		return s.saveLocked()
	}
	return errors.New("代理配置不存在")
}

func (s *Store) AddHistory(entry model.HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.History = append([]model.HistoryEntry{entry}, s.state.History...)
	if len(s.state.History) > 100 {
		s.state.History = s.state.History[:100]
	}
	return s.saveLocked()
}

func (s *Store) History() []model.HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]model.HistoryEntry, len(s.state.History))
	copy(result, s.state.History)
	return result
}

func (s *Store) ClearHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.History = nil
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}
