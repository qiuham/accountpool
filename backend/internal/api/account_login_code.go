package api

import (
	"github.com/ham/accountpool/backend/internal/claude"
	"net/http"
	"time"
)

func (s *Server) accountLoginCode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]string{"error": "请使用 POST 请求"})
		return
	}
	var input struct {
		ID int64 `json:"id"`
	}
	if !decodeConsole(w, r, &input) {
		return
	}
	if input.ID <= 0 {
		writeJSON(w, 400, map[string]string{"error": "账号无效"})
		return
	}
	// One mailbox request chain at a time, with a global cooldown after completion.
	if !s.mailMu.TryLock() {
		w.Header().Set("Retry-After", "5")
		writeJSON(w, 429, map[string]string{"error": "正在读取邮箱，请稍后再试"})
		return
	}
	defer s.mailMu.Unlock()
	if time.Now().Before(s.mailNext) {
		w.Header().Set("Retry-After", "5")
		writeJSON(w, 429, map[string]string{"error": "请间隔 5 秒再读取"})
		return
	}
	a, err := s.Store.GetAccountByID(input.ID)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "账号不存在"})
		return
	}
	cfg, err := s.Store.SupplySettings()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "读取收信配置失败"})
		return
	}
	client := claude.NewMailClient()
	client.Base, client.Domain, client.AdminAuth = cfg.MailBase, cfg.RegistrationDomain, cfg.MailPassword
	if !cfg.AcceptsMailbox(a.Email) || client.Base == "" {
		writeJSON(w, 409, map[string]string{"error": "此账号未接入当前收信服务"})
		return
	}
	defer func() { s.mailNext = time.Now().Add(5 * time.Second) }()
	jwt := a.MailJWT
	if jwt == "" {
		jwt, err = client.RecoverMailboxJWT(a.Email)
	} else {
		err = client.ValidateMailboxJWT(jwt, a.Email)
	}
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "邮箱凭据验证失败，请检查邮箱配置"})
		return
	}
	result, err := client.LatestLogin(r.Context(), jwt, time.Now())
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "读取邮箱失败，请稍后重试"})
		return
	}
	writeJSON(w, 200, result)
}
