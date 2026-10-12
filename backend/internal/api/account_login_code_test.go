package api

import (
	"encoding/json"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoginCodeRequiresConsoleSession(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	st := store.New(db)
	id, e := st.CreateAccount(&store.Account{Name: "fixture", State: "registered", OrgID: "fixture-org", SessionKey: "fixture-secret"})
	if e != nil || id != 1 {
		t.Fatal(e, id)
	}
	if e := st.UpdateFields(id, map[string]any{"org_id": "fixture-org", "session_key": "fixture-secret"}); e != nil {
		t.Fatal(e)
	}
	token, e := st.CreateConsoleSession()
	if e != nil {
		t.Fatal(e)
	}
	mail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/settings" {
			json.NewEncoder(w).Encode(map[string]string{"address": "fixture@example.test"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"id": 1, "subject": "Claude verification code", "text": "Your code: 123456", "created_at": time.Now().UTC().Format(time.RFC3339)}}})
	}))
	defer mail.Close()
	cfg, err := st.SupplySettings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.MailBase, cfg.RegistrationDomain = mail.URL, "example.test"
	cfg.Domains = []string{"example.test", "second.test"}
	if err = st.SaveSupplySettings(cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateFields(id, map[string]any{"email": "fixture@example.test", "mail_jwt": "fixture-jwt"}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, "", "")
	call := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", "http://example.com")
		if auth {
			r.AddCookie(&http.Cookie{Name: "pool_session", Value: token})
		}
		w := httptest.NewRecorder()
		s.consoleData(w, r)
		return w
	}

	if w := call("POST", "/console/accounts/login-code", `{"id":1}`, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := call("POST", "/console/accounts/login-code", `{"id":1}`, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"123456"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("code contract", w.Code)
	}
	if w := call("POST", "/console/accounts/login-code", `{"id":1}`, true); w.Code != 429 {
		t.Fatal("missing cooldown", w.Code)
	}
	if w := call("GET", "/console/accounts/login-code", "", true); w.Code != 405 {
		t.Fatal(w.Code)
	}
}
