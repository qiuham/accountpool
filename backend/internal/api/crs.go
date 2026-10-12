// Package api 提供对外接口。**我们只提供接口，不主动推送。**
//
// 为什么是 pull 而不是 push（DESIGN §2）
// ----------------------------------------
// push 的话我们要持有 sub2api 的 admin key、自己维护幂等键与重试、
// 还要处理"本地已建但远程失败"的中间态。
// pull 的话 sub2api 用它的 crs_account_id 匹配做 upsert，**同步复杂度全被它吸收**——
// 我们只要保证"导出的是当前应该存在的号的状态"。
//
// 所以这里实现 CRS 兼容的两个端点，让 sub2api 以为在跟一台 CRS 同步：
//
//	POST /web/auth/login                      登录拿 token
//	GET  /admin/sync/export-accounts          导出账号（带凭据）
//
// 契约是从 sub2api 的 internal/service/crs_sync_service.go 逐行核对来的，不是猜的。
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ham/accountpool/backend/internal/presentation"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ham/accountpool/backend/internal/pool"
	"github.com/ham/accountpool/backend/internal/store"
)

// CRSProxy 是导出里的出口信息。sub2api 会用它建代理记录并绑定到账号，
// **这一步不做的话，所有号在它那边共用默认出口，前面所有隔离在出口层被抹平**。
type CRSProxy struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// CRSAccount 是导出条目。字段名必须跟 sub2api 的 crsClaudeAccount 完全一致。
type CRSAccount struct {
	Kind        string         `json:"kind"`
	ID          string         `json:"id"` // **匹配键**，sub2api 靠它 upsert
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Platform    string         `json:"platform"`
	AuthType    string         `json:"authType"` // oauth / setup-token
	IsActive    bool           `json:"isActive"`
	Schedulable bool           `json:"schedulable"`
	Priority    int            `json:"priority"`
	Status      string         `json:"status"`
	Proxy       *CRSProxy      `json:"proxy"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra"`
}

// Server 是对外接口。
type Server struct {
	mailMu       sync.Mutex
	mailNext     time.Time
	SetupKey     string
	consoleLimit consoleLimiter
	Store        *store.Store
	Username     string
	Password     string

	mu     sync.Mutex
	tokens map[string]time.Time // token → 过期时间
}

func NewServer(st *store.Store, user, pass string) *Server {
	return &Server{Store: st, Username: user, Password: pass,
		tokens: map[string]time.Time{}}
}

// Routes 挂路由。路径**故意做成 CRS 的形状**，让 sub2api 的现成客户端能用。
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/console/", s.consoleData)
	mux.HandleFunc("/console/auth/", s.consoleAuth)
	mux.HandleFunc("/console/settings/runtime", func(w http.ResponseWriter, r *http.Request) {
		if s.consoleAuthorized(w, r) {
			s.runtimeSettings(w, r)
		}
	})
	mux.HandleFunc("/web/auth/login", s.handleLogin)
	mux.HandleFunc("/web/auth/logout", s.handleLogout)

	mux.HandleFunc("/admin/sync/export-accounts", s.handleExport)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "error": "POST only"})
		return
	}
	if s.Username == "" || s.Password == "" {
		writeJSON(w, 503, map[string]any{"success": false, "error": "login not configured"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("trailing data")
		}
	}
	if err != nil {
		code := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			code = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, code, map[string]any{"success": false, "error": "bad json"})
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(s.Username))
	passOK := subtle.ConstantTimeCompare([]byte(req.Password), []byte(s.Password))
	if userOK&passOK != 1 {
		// 不改提示措辞：不区分"用户不存在"和"密码错"，避免探测
		writeJSON(w, 200, map[string]any{
			"success": false, "error": "invalid credentials"})
		return
	}
	tok, err := s.issueToken()
	if err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "error": "token generation failed"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"success": true, "token": tok, "username": req.Username})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]any{"success": false, "error": "GET only"})
		return
	}
	if !s.validToken(bearer(r)) {
		writeJSON(w, 401, map[string]any{"success": false, "error": "unauthorized"})
		return
	}

	accts, err := s.Store.ListAccounts()
	if err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "error": err.Error()})
		return
	}

	out := make([]CRSAccount, 0, len(accts))
	for _, a := range accts {
		full, err := s.Store.GetAccount(a.Name)
		if err != nil {
			writeJSON(w, 500, map[string]any{"success": false, "error": "account read failed"})
			return
		}
		// CRS cannot import session-only credentials. Retain OAuth tombstones so
		// the downstream upsert explicitly disables previously imported accounts.
		if full.AccessToken == "" || full.RefreshToken == "" {
			continue
		}
		proxy, err := s.Store.AccountProxy(full.ID)
		if err != nil {
			writeJSON(w, 500, map[string]any{"success": false, "error": "proxy read failed"})
			return
		}
		item := s.toCRS(full)
		if proxy != nil {
			u, port, err := store.ParseProxyURL(proxy.URL)
			if err != nil {
				writeJSON(w, 500, map[string]any{"success": false, "error": "invalid bound proxy"})
				return
			}
			item.Proxy = &CRSProxy{Protocol: u.Scheme, Host: u.Hostname(), Port: port}
			if u.User != nil {
				item.Proxy.Username = u.User.Username()
				item.Proxy.Password, _ = u.User.Password()
			}
			if proxy.State == "burned" {
				item.IsActive = false
				item.Schedulable = false
			}
		}
		out = append(out, item)
	}

	writeJSON(w, 200, map[string]any{
		"success": true,
		"data": map[string]any{
			"exportedAt":     time.Now().Format(time.RFC3339),
			"claudeAccounts": out,
		},
	})
}

func (s *Server) toCRS(a *store.Account) CRSAccount {
	st := pool.State(a.State)
	active := st.Usable()

	creds := map[string]any{
		"access_token":  a.AccessToken,
		"refresh_token": a.RefreshToken,
		"expires_at":    a.ExpiresAt / 1000,
		"org_uuid":      a.OrgID,
	}
	if a.Scopes != "" {
		var scopes []string
		if json.Unmarshal([]byte(a.Scopes), &scopes) == nil {
			creds["scopes"] = scopes
		}
	}

	return CRSAccount{
		Kind:        "claude",
		ID:          a.Name, // 稳定 id，sub2api 靠它 upsert（天然幂等）
		Name:        a.Name,
		Description: a.Email,
		Platform:    "claude",
		AuthType:    "oauth",
		IsActive:    active,
		Schedulable: active,
		Priority:    0,
		Status:      a.State,
		Proxy:       nil,
		Credentials: creds,
		Extra: map[string]any{
			"email":           a.Email,
			"plan":            a.Plan,
			"rate_limit_tier": a.RateLimitTier,
			"org_id":          a.OrgID,
			"warmup_count":    a.WarmupCount,
		},
	}
}

// ── token ──
//
// 用随机 token + 内存表，不用 JWT：**这样重启即全部失效**，
// 而 sub2api 是定期来拉、每次都会重新登录，所以重启失效没有代价。
func (s *Server) issueToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	now := time.Now()
	for token, expires := range s.tokens {
		if !expires.After(now) {
			delete(s.tokens, token)
		}
	}
	// 防止频繁登录导致内存无限增长；现有令牌保持有效。
	if len(s.tokens) >= 1024 {
		s.mu.Unlock()
		return "", errors.New("too many active sessions")
	}
	s.tokens[tok] = now.Add(24 * time.Hour)
	s.mu.Unlock()
	return tok, nil
}

func (s *Server) validToken(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[tok]
	if !ok || time.Now().After(exp) {
		delete(s.tokens, tok)
		return false
	}
	return true
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	if body, ok := v.(map[string]string); ok && body["error"] != "" {
		body["error"] = presentation.Reason(body["error"])
	}
	if body, ok := v.(map[string]any); ok {
		if message, ok := body["error"].(string); ok && message != "" {
			body["error"] = presentation.Reason(message)
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
