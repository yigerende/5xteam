package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"
)

const authCookieName = "space_session"

type loginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (s *Server) createSession() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	s.authMu.Lock()
	s.sessions[token] = time.Now().Add(24 * time.Hour)
	s.authMu.Unlock()
	return token
}

func (s *Server) validSession(r *http.Request) bool {
	cookie, err := r.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	expires, ok := s.sessions[cookie.Value]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(s.sessions, cookie.Value)
		return false
	}
	return true
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" || strings.HasPrefix(path, "/assets/") || path == "/health/live" || path == "/health/ready" || path == "/api/auth/login" || path == "/api/auth/me" || path == "/api/auth/logout" {
			next.ServeHTTP(w, r)
			return
		}
		if path == "/api/integrations/turb/register" {
			username, password, ok := r.BasicAuth()
			if !ok || !s.store.CheckUserPassword(username, password) {
				w.Header().Set("WWW-Authenticate", `Basic realm="turb-gpt-free-register"`)
				writeAPI(w, http.StatusUnauthorized, nil, "远程注册服务认证失败")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !s.validSession(r) {
			writeAPI(w, http.StatusUnauthorized, nil, "请先登录")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input loginInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	if !s.store.CheckUserPassword(strings.TrimSpace(input.Username), input.Password) {
		writeAPI(w, http.StatusUnauthorized, nil, "用户名或密码错误")
		return
	}
	token := s.createSession()
	if token == "" {
		writeAPI(w, http.StatusInternalServerError, nil, "创建登录会话失败")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	writeAPI(w, http.StatusOK, map[string]string{"username": strings.TrimSpace(input.Username)}, "")
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	if !s.validSession(r) {
		writeAPI(w, http.StatusUnauthorized, nil, "未登录")
		return
	}
	writeAPI(w, http.StatusOK, map[string]string{"username": "admin"}, "")
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(authCookieName); err == nil {
		s.authMu.Lock()
		delete(s.sessions, cookie.Value)
		s.authMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeAPI(w, http.StatusOK, map[string]bool{"logged_out": true}, "")
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var input passwordInput
	if err := decodeJSON(w, r, &input, 1<<20); err != nil {
		return
	}
	if input.NewPassword != input.ConfirmPassword {
		writeAPI(w, http.StatusBadRequest, nil, "两次输入的新密码不一致")
		return
	}
	if err := s.store.ChangeUserPassword("admin", input.CurrentPassword, input.NewPassword); err != nil {
		writeAPI(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	// Password changes invalidate all existing sessions.
	s.authMu.Lock()
	s.sessions = make(map[string]time.Time)
	s.authMu.Unlock()
	writeAPI(w, http.StatusOK, map[string]bool{"changed": true}, "")
}
