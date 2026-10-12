package main

import (
	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/pool"
	"github.com/ham/accountpool/backend/internal/runner"
	"github.com/ham/accountpool/backend/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMailboxTracePersistsForTask(t *testing.T) {
	root := t.TempDir()
	db, e := store.Open(filepath.Join(root, "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	st := store.New(db)
	a, e := st.CreateAccount(&store.Account{Name: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	result, e := db.Exec("INSERT INTO jobs(account_id,kind,state,created_at) VALUES(?,'register','running','2026-10-08')", a)
	if e != nil {
		t.Fatal(e)
	}
	id, _ := result.LastInsertId()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"address":"fixture@example.test","jwt":"private-jwt","address_id":1}`))
	}))
	defer srv.Close()
	mail := claude.NewMailClient()
	mail.Base = srv.URL
	mail.AdminAuth = "private-admin"
	mail.Domain = "example.test"
	sch := &pool.Scheduler{}
	r := runner.New(root)
	configureTaskTrace(st, sch, r, mail)
	done := sch.StartTrace(id)
	address, e := mail.NewAddress("fixture")
	if e != nil || address.JWT != "private-jwt" {
		t.Fatal("capture changed behavior")
	}
	path := r.TraceFile
	done()
	var raw string
	if e = db.QueryRow("SELECT payload FROM job_http_traces WHERE job_id=?", id).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(raw, "private-jwt") || strings.Contains(raw, "private-admin") {
		t.Fatal("secret persisted")
	}
	if !strings.Contains(raw, "/api/admin/new_address") || !strings.Contains(raw, `"status":200`) {
		t.Fatal("missing request/response")
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("temporary trace not removed")
	}
}
