package api

import (
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsoleSetupSessionsAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	s := NewServer(st, "export", "export-pass")
	s.SetupKey = "setup-fixture"
	mux := http.NewServeMux()
	s.Routes(mux)
	call := func(method, path, body, cookie, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Origin", origin)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	payload := `{"username":"admin","password":"a-long-test-password","setupKey":"setup-fixture"}`
	if w := call("POST", "/console/auth/setup", payload, "", "http://evil.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := call("POST", "/console/auth/setup", payload, "", "http://example.com")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("bad cookie")
	}
	cookie := cookies[0].Name + "=" + cookies[0].Value
	if w := call("POST", "/console/auth/setup", payload, "", "http://example.com"); w.Code != 409 {
		t.Fatal("reinitialization allowed")
	}
	if w := call("GET", "/console/accounts", "", "", ""); w.Code != 401 {
		t.Fatal("unauthenticated read")
	}
	if w := call("GET", "/console/accounts", "", cookie, ""); w.Code != 200 || w.Body.String() != "[]\n" {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("PUT", "/console/settings/runtime", `{}`, cookie, "http://evil.example"); w.Code != 403 {
		t.Fatal("cross origin allowed")
	}
	token, _ := s.issueToken()
	req := httptest.NewRequest("GET", "/console/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	mux.ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatal("export token grants console access")
	}
	db.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.Store = store.New(db)
	if !s.Store.ConsoleSession(cookies[0].Value) {
		t.Fatal("session not persisted")
	}
	if w := call("POST", "/console/auth/logout", "", cookie, "http://example.com"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call("GET", "/console/accounts", "", cookie, ""); w.Code != 401 {
		t.Fatal("logout failed")
	}
}
func TestSupplySecretNotReturned(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	token, _ := st.CreateConsoleSession()
	s := NewServer(st, "", "")
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/console/settings/supply", strings.NewReader(body))
		r.Header.Set("Origin", "http://example.com")
		r.AddCookie(&http.Cookie{Name: "pool_session", Value: token})
		w := httptest.NewRecorder()
		s.consoleData(w, r)
		return w
	}
	body := `{"target":0,"daily":3,"checkHours":24,"prefix":"free-","mailBase":"https://mail.example.com","registrationDomain":"example.com","domains":["example.com"],"mailPassword":"secret-fixture"}`
	w := call(body)
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-fixture") {
		t.Fatal(w.Code, w.Body)
	}
	w = call(strings.Replace(body, `,"mailPassword":"secret-fixture"`, "", 1))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	v, _ := st.SupplySettings()
	if v.MailPassword != "secret-fixture" {
		t.Fatal("secret not retained")
	}
}

func TestAccountViewUsesFullRecordWithoutSecrets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	id, err := st.CreateAccount(&store.Account{Name: "fixture", Email: "fixture@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE accounts SET session_key=?,access_token=? WHERE id=?", "session-secret", "oauth-secret", id)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := st.CreateConsoleSession()
	s := NewServer(st, "", "")
	r := httptest.NewRequest("GET", "/console/accounts", nil)
	r.AddCookie(&http.Cookie{Name: "pool_session", Value: token})
	w := httptest.NewRecorder()
	s.consoleData(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"session":true`) || !strings.Contains(w.Body.String(), `"id":1`) || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, w.Body)
	}
}
func TestPasswordResetRevokesAllSessions(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	if err := st.CreateConsoleAdmin("admin", "original-long-password"); err != nil {
		t.Fatal(err)
	}
	token, _ := st.CreateConsoleSession()
	if err := st.ResetConsolePassword("replacement-password"); err != nil {
		t.Fatal(err)
	}
	if st.ConsoleSession(token) || st.CheckConsolePassword("admin", "original-long-password") || !st.CheckConsolePassword("admin", "replacement-password") {
		t.Fatal("reset failed")
	}
}
