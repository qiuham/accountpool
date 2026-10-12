package api

import (
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionCopyRequiresConsoleSession(t *testing.T) {
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
	if w := call("POST", "/console/accounts/session-key", `{"id":1}`, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := call("POST", "/console/accounts/session-key", `{"id":1}`, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "fixture-secret") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("copy contract failed", w.Code)
	}
	w = call("GET", "/console/accounts", "", true)
	if w.Code != 200 || strings.Contains(w.Body.String(), "fixture-secret") || !strings.Contains(w.Body.String(), `"orgId":"fixture-org"`) {
		t.Fatal("list leaked key or omitted org")
	}
}
