package api

import (
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestJobDetailRedactsAndRequiresLogin(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	st := store.New(db)
	a, e := st.CreateAccount(&store.Account{Name: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`INSERT INTO jobs(account_id,kind,state,attempts,reason,detail,created_at) VALUES(?,'register','failed',1,'network-error',?,'2026-10-08 10:00:00')`, a, `{"error":{"message":"connection reset"},"sessionKey":"secret-fixture","nested":{"password":"password-fixture"},"url":"https://user:pass@example.test/path?token=sensitive"}`)
	if e != nil {
		t.Fatal(e)
	}
	token, e := st.CreateConsoleSession()
	if e != nil {
		t.Fatal(e)
	}
	s := NewServer(st, "", "")
	call := func(path string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if auth {
			r.AddCookie(&http.Cookie{Name: "pool_session", Value: token})
		}
		w := httptest.NewRecorder()
		s.consoleData(w, r)
		return w
	}
	if w := call("/console/jobs/detail?id=1", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := call("/console/jobs/detail?id=1", true)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, secret := range []string{"secret-fixture", "password-fixture", "user:pass", "token=sensitive"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret leaked")
		}
	}
	if !strings.Contains(w.Body.String(), "connection reset") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing diagnosis or no-store")
	}
	if w := call("/console/jobs/detail?id=-1", true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := call("/console/jobs/detail?id=999", true); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if _, e = db.Exec("INSERT INTO cleared_job_history(job_id) VALUES(1)"); e != nil {
		t.Fatal(e)
	}
	if w := call("/console/jobs/detail?id=1", true); w.Code != 404 {
		t.Fatal("cleared record exposed")
	}
}
