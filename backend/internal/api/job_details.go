package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var secretToken = regexp.MustCompile(`sk-[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
var secretLine = regexp.MustCompile(`(?im)(authorization|cookie|set-cookie|session_?key|access_?token|refresh_?token|password|jwt|x-admin-auth)\s*[:=]\s*[^\r\n]+`)
var detailURL = regexp.MustCompile(`https?://[^\s"'<>]+`)

func redactJobText(s string) string {
	s = secretToken.ReplaceAllString(s, "[REDACTED]")
	s = secretLine.ReplaceAllString(s, "${1}: [REDACTED]")
	return detailURL.ReplaceAllStringFunc(s, func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil {
			return "[URL]"
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	})
}
func redactJobValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			key := strings.ToLower(k)
			if strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "cookie") || strings.Contains(key, "session") || strings.Contains(key, "credential") || key == "authorization" || key == "jwt" || key == "x-admin-auth" {
				x[k] = "[REDACTED]"
			} else {
				x[k] = redactJobValue(v)
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = redactJobValue(x[i])
		}
		return x
	case string:
		return redactJobText(x)
	}
	return v
}
func (s *Server) consoleJobDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]string{"error": "请求方法不支持"})
		return
	}
	id, e := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if e != nil || id <= 0 {
		writeJSON(w, 400, map[string]string{"error": "记录编号无效"})
		return
	}
	var accountID sql.NullInt64
	var email, name, kind, state, reason, detail, started, finished string
	var attempts int
	e = s.Store.DB().QueryRow(`SELECT j.account_id,COALESCE(a.email,''),COALESCE(a.name,''),j.kind,j.state,COALESCE(j.reason,''),COALESCE(j.detail,''),j.attempts,COALESCE(j.started_at,''),COALESCE(j.finished_at,'') FROM jobs j LEFT JOIN accounts a ON a.id=j.account_id WHERE j.id=? AND j.id NOT IN(SELECT job_id FROM cleared_job_history)`, id).Scan(&accountID, &email, &name, &kind, &state, &reason, &detail, &attempts, &started, &finished)
	if e == sql.ErrNoRows {
		writeJSON(w, 404, map[string]string{"error": "执行记录不存在"})
		return
	}
	if e != nil {
		writeJSON(w, 500, map[string]string{"error": "读取执行记录失败"})
		return
	}
	var payload any = detail
	if detail != "" {
		var parsed any
		if json.Unmarshal([]byte(detail), &parsed) == nil {
			payload = parsed
		}
	}
	var raw string
	traces := []any{}
	e = s.Store.DB().QueryRow("SELECT payload FROM job_http_traces WHERE job_id=? AND captured_at>?", id, time.Now().Add(-7*24*time.Hour).Unix()).Scan(&raw)
	if e == nil {
		if json.Unmarshal([]byte(raw), &traces) != nil {
			writeJSON(w, 500, map[string]string{"error": "报文读取失败"})
			return
		}
	} else if e != sql.ErrNoRows {
		writeJSON(w, 500, map[string]string{"error": "报文读取失败"})
		return
	}

	writeJSON(w, 200, map[string]any{"id": id, "email": email, "name": name, "kind": kind, "state": state, "recordExecutions": attempts, "startedAt": started, "finishedAt": finished, "result": map[string]any{"reason": redactJobText(reason), "detail": redactJobValue(payload)}, "httpTraces": traces, "httpCaptureAvailable": len(traces) > 0})
}
