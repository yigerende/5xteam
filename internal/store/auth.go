package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

const passwordIterations = 120000

func hashPassword(password string, salt []byte) string {
	digest := append([]byte(nil), salt...)
	for i := 0; i < passwordIterations; i++ {
		h := sha256.New()
		h.Write(digest)
		h.Write([]byte(password))
		digest = h.Sum(nil)
	}
	return base64.RawStdEncoding.EncodeToString(digest)
}

func (s *Store) EnsureAdminUser() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM app_users").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	_, err := s.db.Exec("INSERT INTO app_users(username, password_hash, password_salt, updated_at) VALUES(?, ?, ?, ?)", "admin", hashPassword("admin", salt), base64.RawStdEncoding.EncodeToString(salt), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) CheckUserPassword(username, password string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var encodedHash, encodedSalt string
	if err := s.db.QueryRow("SELECT password_hash, password_salt FROM app_users WHERE username=?", strings.TrimSpace(username)).Scan(&encodedHash, &encodedSalt); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(encodedSalt)
	if err != nil {
		return false
	}
	actual := hashPassword(password, salt)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(encodedHash)) == 1
}

func (s *Store) ChangeUserPassword(username, currentPassword, newPassword string) error {
	if !s.CheckUserPassword(username, currentPassword) {
		return errors.New("当前密码错误")
	}
	if len([]rune(newPassword)) < 6 {
		return errors.New("新密码至少需要 6 个字符")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("UPDATE app_users SET password_hash=?, password_salt=?, updated_at=? WHERE username=?", hashPassword(newPassword, salt), base64.RawStdEncoding.EncodeToString(salt), time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(username))
	return err
}
