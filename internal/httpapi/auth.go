package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
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
		if path == "/api/pro-takeover/peer" {
			username, password, ok := r.BasicAuth()
			if !ok || !s.store.CheckUserPassword(username, password) {
				writeAPI(w, http.StatusUnauthorized, nil, "对方 Space 登录认证失败")
				return
			}
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
		// Financial endpoints must reject cross-origin cookie requests, including
		// same-site sibling origins that SameSite=Lax alone does not block.
		if (strings.HasPrefix(path, "/api/ejh/") || strings.HasPrefix(path, "/api/nexovcc/") || strings.HasPrefix(path, "/api/mail/gptregister/") || strings.HasPrefix(path, "/api/sub2-proxy-binding-")) && r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := r.Header.Get("Origin")
			u, err := url.Parse(origin)
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" || (origin != "" && (err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https"))) {
				writeAPI(w, http.StatusForbidden, nil, "拒绝跨站资金操作")
				return
			}
			if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
				writeAPI(w, http.StatusUnsupportedMediaType, nil, "卡台操作只接受 JSON")
				return
			}
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
