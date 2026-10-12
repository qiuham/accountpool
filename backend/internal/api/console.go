package api

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type consoleLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func (l *consoleLimiter) allow(addr string) bool {
	host, _, _ := net.SplitHostPort(addr)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = map[string][]time.Time{}
	}
	now := time.Now()
	for key, times := range l.attempts {
		kept := times[:0]
		for _, t := range times {
			if now.Sub(t) < time.Minute {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(l.attempts, key)
		} else {
			l.attempts[key] = kept
		}
	}
	if len(l.attempts[host]) >= 10 || len(l.attempts) >= 4096 {
		return false
	}
	l.attempts[host] = append(l.attempts[host], now)
	return true
}
func decodeConsole(w http.ResponseWriter, r *http.Request, v any) bool {
	limit := int64(16384)
	if r.URL.Path == "/console/settings/activity" {
		limit = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "trailing JSON"})
		return false
	}
	return true
}
func consoleToken(r *http.Request) string {
	c, e := r.Cookie("pool_session")
	if e != nil {
		return ""
	}
	return c.Value
}
func (s *Server) consoleAuthorized(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" {
		origin, err := url.Parse(r.Header.Get("Origin"))
		if err != nil || origin.Host != r.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
			writeJSON(w, 403, map[string]string{"error": "invalid origin"})
			return false
		}
	}
	if !s.Store.ConsoleSession(consoleToken(r)) {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}
func (s *Server) consoleAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ready, err := s.Store.ConsoleReady()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "database unavailable"})
		return
	}
	if r.Method == "GET" && r.URL.Path == "/console/auth/status" {
		writeJSON(w, 200, map[string]bool{"initialized": ready, "authenticated": s.Store.ConsoleSession(consoleToken(r))})
		return
	}
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]string{"error": "POST only"})
		return
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host != r.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
		writeJSON(w, 403, map[string]string{"error": "invalid origin"})
		return
	}
	if r.URL.Path == "/console/auth/logout" {
		if err := s.Store.DeleteConsoleSession(consoleToken(r)); err != nil {
			writeJSON(w, 500, map[string]string{"error": "logout failed"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "pool_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(w, 200, map[string]bool{"success": true})
		return
	}
	if !s.consoleLimit.allow(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, 429, map[string]string{"error": "登录尝试过多，请稍后重试"})
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		SetupKey string `json:"setupKey,omitempty"`
	}
	if !decodeConsole(w, r, &input) {
		return
	}
	switch r.URL.Path {
	case "/console/auth/setup":
		if ready {
			writeJSON(w, 409, map[string]string{"error": "管理员已初始化"})
			return
		}
		if s.SetupKey == "" || subtle.ConstantTimeCompare([]byte(input.SetupKey), []byte(s.SetupKey)) != 1 {
			writeJSON(w, 403, map[string]string{"error": "初始化密钥不正确"})
			return
		}
		if err := s.Store.CreateConsoleAdmin(input.Username, input.Password); err != nil {
			writeJSON(w, 400, map[string]string{"error": "初始化失败，密码至少 12 字符；管理员只能创建一次"})
			return
		}
	case "/console/auth/login":
		if !ready || !s.Store.CheckConsolePassword(input.Username, input.Password) {
			writeJSON(w, 401, map[string]string{"error": "用户名或密码错误"})
			return
		}
	default:
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	token, err := s.Store.CreateConsoleSession()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "session creation failed"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "pool_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	writeJSON(w, 200, map[string]bool{"success": true})
}
