package api

import (
	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/lifecycle"
	"github.com/ham/accountpool/backend/internal/pool"
	"github.com/ham/accountpool/backend/internal/presentation"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func (s *Server) consoleData(w http.ResponseWriter, r *http.Request) {
	if !s.consoleAuthorized(w, r) {
		return
	}
	fail := func() { writeJSON(w, 500, map[string]string{"error": "读取或更新失败"}) }
	switch r.URL.Path {
	case "/console/executor":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		status, err := s.Store.ExecutorStatus()
		if err != nil {
			fail()
			return
		}
		writeJSON(w, 200, status)
		return
	case "/console/accounts/login-code":
		s.accountLoginCode(w, r)
		return
	case "/console/accounts/session-key":
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "POST" {
			break
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
		a, err := s.Store.GetAccountByID(input.ID)
		if err != nil {
			writeJSON(w, 404, map[string]string{"error": "账号不存在"})
			return
		}
		if a.SessionKey == "" {
			writeJSON(w, 409, map[string]string{"error": "此账号暂无 SessionKey"})
			return
		}
		writeJSON(w, 200, map[string]string{"sessionKey": a.SessionKey})
		return
	case "/console/password":
		if r.Method != "POST" {
			break
		}
		var input struct {
			Username string `json:"username"`
			Current  string `json:"current"`
			Password string `json:"password"`
		}
		if !decodeConsole(w, r, &input) {
			return
		}
		if !s.consoleLimit.allow(r.RemoteAddr) {
			writeJSON(w, 429, map[string]string{"error": "请求过多，请稍后重试"})
			return
		}
		if !s.Store.CheckConsolePassword(input.Username, input.Current) {
			writeJSON(w, 403, map[string]string{"error": "当前账号密码错误"})
			return
		}
		if len(input.Password) < 12 || len(input.Password) > 256 {
			writeJSON(w, 400, map[string]string{"error": "新密码需为 12–256 字符"})
			return
		}
		if s.Store.ResetConsolePassword(input.Password) != nil {
			fail()
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "pool_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(w, 200, map[string]bool{"success": true})
		return
	case "/console/accounts/restore-mailbox":
		if r.Method != "POST" {
			break
		}
		var input struct {
			ID int64 `json:"id"`
		}
		if !decodeConsole(w, r, &input) {
			return
		}
		if !s.consoleLimit.allow(r.RemoteAddr) {
			writeJSON(w, 429, map[string]string{"error": "请求过多，请稍后重试"})
			return
		}
		cfg, err := s.Store.SupplySettings()
		if err != nil {
			fail()
			return
		}
		mail := claude.NewMailClient()
		mail.Base = cfg.MailBase
		account, err := s.Store.GetAccountByID(input.ID)
		if err != nil {
			writeJSON(w, 404, map[string]string{"error": "账号不存在"})
			return
		}
		if !cfg.AcceptsMailbox(account.Email) {
			writeJSON(w, 409, map[string]string{"error": "收件邮箱不属于已配置的邮箱域名"})
			return
		}
		_, mail.Domain, _ = strings.Cut(account.Email, "@")
		mail.AdminAuth = cfg.MailPassword
		if err = lifecycle.RestoreMailbox(s.Store, mail, input.ID); err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"success": true})
		return
	case "/console/accounts/maintenance":
		if r.Method != "POST" {
			break
		}
		var input struct {
			IDs    []int64 `json:"ids"`
			Paused *bool   `json:"paused"`
		}
		if !decodeConsole(w, r, &input) {
			return
		}
		if input.Paused == nil {
			writeJSON(w, 400, map[string]string{"error": "缺少维护状态"})
			return
		}
		if err := s.Store.SetAccountsPaused(input.IDs, *input.Paused); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"success": true})
		return
	case "/console/pool":
		if r.Method != "GET" {
			break
		}
		supply, err := s.Store.SupplySettings()
		if err != nil {
			fail()
			return
		}
		policy := pool.Maintenance{TargetFree: supply.Target, CheckInterval: time.Duration(supply.CheckHours) * time.Hour}
		var inventory pool.Inventory
		paused, blocked := 0, 0
		err = s.Store.ReconcileMaintenance(func(accounts []store.MaintenanceAccount) (*store.MaintenanceAction, error) {
			inventory = policy.Summary(accounts, time.Now())
			for _, a := range accounts {
				if a.AccessToken != "" || a.RefreshToken != "" || a.SubscribedAt != "" || a.Plan != "" && a.Plan != "claude_free" || a.RetiredAt != "" {
					continue
				}
				if a.Paused {
					paused++
				} else if a.State == "mailbox_missing" || a.State == "mailbox_config_error" {
					blocked++
				}
			}
			return nil, nil
		})
		if err != nil {
			fail()
			return
		}
		var running, queued int
		var nextRetry int64
		if err = s.Store.DB().QueryRow("SELECT COUNT(*) FROM jobs WHERE state='running'").Scan(&running); err != nil {
			fail()
			return
		}
		if err = s.Store.DB().QueryRow("SELECT COUNT(*) FROM jobs WHERE state='pending'").Scan(&queued); err != nil {
			fail()
			return
		}
		if err = s.Store.DB().QueryRow("SELECT COALESCE(MIN(due_at),0) FROM account_retries WHERE account_id NOT IN (SELECT account_id FROM account_maintenance_pauses)").Scan(&nextRetry); err != nil {
			fail()
			return
		}
		writeJSON(w, 200, map[string]any{"target": inventory.Target, "ready": inventory.Free, "recovering": max(0, inventory.Reserved-paused-blocked), "pausedAccounts": paused, "blocked": blocked + inventory.Unavailable, "paid": inventory.Paid, "deficit": max(0, inventory.Target-inventory.Free), "pendingSupply": max(0, inventory.Target-inventory.Free-inventory.Reserved), "createdToday": inventory.CreatedToday, "dailyLimit": supply.Daily, "running": running, "queued": queued, "nextRetry": nextRetry})
		return
	case "/console/accounts":
		if r.Method != "GET" {
			break
		}
		accounts, err := s.Store.ListAccounts()
		if err != nil {
			fail()
			return
		}
		supply, err := s.Store.SupplySettings()
		if err != nil {
			fail()
			return
		}
		platformPause, err := s.Store.SchedulerPause()
		if err != nil {
			fail()
			return
		}
		poolProxies, err := s.Store.ListProxies()
		if err != nil {
			fail()
			return
		}
		registrationAt, err := s.Store.RegistrationReadyAt(supply.RegistrationGapMinutes)
		if err != nil {
			fail()
			return
		}
		activity, err := s.Store.ActivitySettings()
		if err != nil {
			fail()
			return
		}
		var chatInterval time.Duration
		if activity.Enabled {
			chatInterval = time.Duration(activity.IntervalHours) * time.Hour
		}
		snapshots := map[int64]store.MaintenanceAccount{}
		err = s.Store.ReconcileMaintenance(func(items []store.MaintenanceAccount) (*store.MaintenanceAction, error) {
			for _, a := range items {
				snapshots[a.ID] = a
			}
			return nil, nil
		})
		if err != nil {
			fail()
			return
		}
		out := []map[string]any{}
		for _, summary := range accounts {
			a, err := s.Store.GetAccount(summary.Name)
			if err != nil {
				fail()
				return
			}
			p, err := s.Store.AccountProxy(a.ID)
			if err != nil {
				fail()
				return
			}
			var proxy any
			if p != nil {
				proxy = p.ID
			}
			retryAt, err := s.Store.AccountRetryTime(a.ID)
			if err != nil {
				fail()
				return
			}
			var warmupCount, warmupDays int
			err = s.Store.DB().QueryRow(`SELECT COUNT(*), COUNT(DISTINCT substr(at,1,10)) FROM events WHERE account_id=? AND kind='warmup' AND json_valid(detail) AND json_extract(detail,'$.status')='ok'`, a.ID).Scan(&warmupCount, &warmupDays)
			if err != nil {
				fail()
				return
			}
			var orgCreatedAt string
			err = s.Store.DB().QueryRow(`SELECT COALESCE((SELECT json_extract(detail,'$.orgCreatedAt') FROM events WHERE account_id=? AND kind IN ('register','check') AND json_valid(detail) AND json_extract(detail,'$.orgId')=? AND COALESCE(json_extract(detail,'$.orgCreatedAt'),'')!='' ORDER BY id DESC LIMIT 1),'')`, a.ID, a.OrgID).Scan(&orgCreatedAt)
			if err != nil {
				fail()
				return
			}
			paused, err := s.Store.AccountMaintenancePaused(a.ID)
			if err != nil {
				fail()
				return
			}
			evidence, err := s.Store.RegistrationEvidence(a.ID)
			if err != nil {
				fail()
				return
			}
			snapshot := snapshots[a.ID]
			snapshot.Account = *a
			status, statusReason := accountStatus(snapshot)
			var proxyAt int64
			if p != nil && p.CooldownUntil != "" {
				if t, e := time.ParseInLocation("2006-01-02 15:04:05", p.CooldownUntil, time.Local); e == nil {
					proxyAt = t.Unix()
				}
			}
			schedule := accountSchedule(snapshot, platformPause != "", time.Now(), registrationAt, proxyAt, time.Duration(supply.CheckHours)*time.Hour, chatInterval)
			if os.Getenv("ACCOUNTPOOL_REQUIRE_PROXY") == "true" && len(poolProxies) == 0 && schedule.Label != "执行中" && schedule.Label != "已停止" && schedule.Label != "已暂停" {
				schedule.Label = "等待代理"
				schedule.Reason = "请先配置并测试账号代理；不会直连 Claude"
				schedule.ReadyAt = 0
			}
			out = append(out, map[string]any{"displayStatus": status, "statusReason": statusReason, "schedule": schedule, "orgId": a.OrgID, "registrationEvidence": evidence, "maintenancePaused": paused, "orgCreatedAt": orgCreatedAt, "warmupCount": warmupCount, "warmupDays": warmupDays, "retryAt": retryAt, "id": a.ID, "email": a.Email, "name": a.Name, "platform": "claude", "state": a.State, "plan": a.Plan, "session": a.SessionKey != "", "oauth": a.AccessToken != "", "checked": a.LastCheckAt, "proxy": proxy})
		}
		writeJSON(w, 200, out)
		return
	case "/console/jobs/detail":
		s.consoleJobDetail(w, r)
		return
	case "/console/jobs":
		if r.Method != "GET" {
			break
		}
		page := 1
		if raw := r.URL.Query().Get("page"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 1000000 {
				writeJSON(w, 400, map[string]string{"error": "页码无效"})
				return
			}
			page = n
		}
		tx, err := s.Store.DB().BeginTx(r.Context(), nil)
		if err != nil {
			fail()
			return
		}
		defer tx.Rollback()
		var total, active int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id NOT IN (SELECT job_id FROM cleared_job_history)`).Scan(&total); err != nil {
			fail()
			return
		}
		if err = tx.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state IN ('pending','running')`).Scan(&active); err != nil {
			fail()
			return
		}
		pages := (total + 19) / 20
		if pages < 1 {
			pages = 1
		}
		if page > pages {
			page = pages
		}
		rows, err := tx.Query(`SELECT j.id,COALESCE(a.email,''),COALESCE(a.name,''),j.kind,j.state,COALESCE(j.reason,''),j.attempts,COALESCE(j.started_at,''),COALESCE(j.finished_at,'') FROM jobs j LEFT JOIN accounts a ON a.id=j.account_id WHERE j.id NOT IN (SELECT job_id FROM cleared_job_history) ORDER BY j.id DESC LIMIT 20 OFFSET ?`, (page-1)*20)
		if err != nil {
			fail()
			return
		}
		out := []map[string]any{}
		for rows.Next() {
			var id, attempts int
			var email, name, kind, state, reason, started, finished string
			if err = rows.Scan(&id, &email, &name, &kind, &state, &reason, &attempts, &started, &finished); err != nil {
				rows.Close()
				fail()
				return
			}
			out = append(out, map[string]any{"id": id, "email": email, "name": name, "kind": kind, "state": state, "reason": reason, "reasonText": presentation.Reason(reason), "attempts": attempts, "startedAt": started, "finishedAt": finished})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			fail()
			return
		}
		if err = tx.Commit(); err != nil {
			fail()
			return
		}
		writeJSON(w, 200, map[string]any{"items": out, "total": total, "page": page, "pageSize": 20, "active": active})
		return

	case "/console/scheduler":
		if r.Method == "POST" {
			var v struct {
				Paused bool `json:"paused"`
			}
			if !decodeConsole(w, r, &v) {
				return
			}
			var err error
			if v.Paused {
				err = s.Store.PauseScheduler("operator-paused", time.Time{})
			} else {
				reason, e := s.Store.SchedulerPause()
				if e != nil {
					fail()
					return
				}
				if reason != "" && reason != "operator-paused" {
					writeJSON(w, 409, map[string]string{"error": "此暂停由执行异常触发，请检查任务后通过 worker --resume 恢复"})
					return
				}
				err = s.Store.ResumeScheduler()
			}
			if err != nil {
				fail()
				return
			}
		} else if r.Method != "GET" {
			break
		}
		reason, err := s.Store.SchedulerPause()
		if err != nil {
			fail()
			return
		}
		writeJSON(w, 200, map[string]any{"paused": reason != "", "reason": reason, "reasonText": presentation.Reason(reason)})
		return
	case "/console/settings/activity":
		v, err := s.Store.ActivitySettings()
		if err != nil {
			fail()
			return
		}
		if r.Method == "PUT" {
			if !decodeConsole(w, r, &v) {
				return
			}
			if err := v.Validate(); err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
			if s.Store.SaveActivitySettings(v) != nil {
				fail()
				return
			}
		} else if r.Method != "GET" {
			break
		}
		writeJSON(w, 200, v)
		return
	case "/console/settings/supply":
		v, err := s.Store.SupplySettings()
		if err != nil {
			fail()
			return
		}
		if r.Method == "PUT" {
			var input struct {
				SequenceDigits         int      `json:"sequenceDigits"`
				RegistrationGapMinutes int      `json:"registrationGapMinutes"`
				Target                 int      `json:"target"`
				Daily                  int      `json:"daily"`
				CheckHours             int      `json:"checkHours"`
				Prefix                 string   `json:"prefix"`
				MailBase               string   `json:"mailBase"`
				RegistrationDomain     string   `json:"registrationDomain"`
				Domains                []string `json:"domains"`
				MailPassword           *string  `json:"mailPassword,omitempty"`
			}
			if !decodeConsole(w, r, &input) {
				return
			}
			secret := v.MailPassword
			if input.MailPassword != nil {
				secret = *input.MailPassword
			}
			v = store.SupplySettings{SequenceDigits: input.SequenceDigits, RegistrationGapMinutes: input.RegistrationGapMinutes, Target: input.Target, Daily: input.Daily, CheckHours: input.CheckHours, Prefix: input.Prefix, MailBase: input.MailBase, RegistrationDomain: input.RegistrationDomain, Domains: input.Domains, MailPassword: secret}
			if err := v.Validate(); err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
			if err := s.Store.SaveSupplySettings(v); err != nil {
				writeJSON(w, 409, map[string]string{"error": err.Error()})
				return
			}
		} else if r.Method != "GET" {
			break
		}
		writeJSON(w, 200, map[string]any{"sequenceDigits": v.SequenceDigits, "registrationGapMinutes": v.RegistrationGapMinutes, "target": v.Target, "daily": v.Daily, "checkHours": v.CheckHours, "prefix": v.Prefix, "mailBase": v.MailBase, "registrationDomain": v.RegistrationDomain, "domains": v.Domains, "mailPasswordConfigured": v.MailPassword != ""})
		return
	case "/console/accounts/proxy":
		if r.Method != "PUT" {
			break
		}
		var input struct {
			ID      int64 `json:"id"`
			ProxyID int64 `json:"proxyId"`
		}
		if !decodeConsole(w, r, &input) {
			return
		}
		if err := s.Store.ChangeAccountProxy(input.ID, input.ProxyID); err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"success": true})
		return
	case "/console/proxies":
		if r.Method == "POST" {
			var input struct {
				URLs []string `json:"urls"`
			}
			if !decodeConsole(w, r, &input) {
				return
			}
			if err := s.Store.ImportProxies(input.URLs); err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
		} else if r.Method != "GET" {
			break
		}
		proxies, err := s.Store.ListProxies()
		if err != nil {
			fail()
			return
		}
		out := []map[string]any{}
		for _, p := range proxies {
			u, e := url.Parse(p.URL)
			if e != nil {
				continue
			}
			configured := u.User != nil
			u.User = nil
			var bound int
			if err := s.Store.DB().QueryRow("SELECT COUNT(*) FROM account_proxies WHERE proxy_id=?", p.ID).Scan(&bound); err != nil {
				fail()
				return
			}
			out = append(out, map[string]any{"boundAccounts": bound, "id": p.ID, "url": u.String(), "credentialsConfigured": configured, "state": p.State, "cooldown": p.CooldownUntil, "failures": p.FailCount, "geo": p.Geo})
		}
		writeJSON(w, 200, out)
		return
	default:
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, 405, map[string]string{"error": "method not allowed"})
}
