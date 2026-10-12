package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyBatchAndExplicitBinding(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db)
	if err = s.ImportProxies([]string{"http://user:secret@host:8080", "invalid-secret"}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid batch or credential leakage", err)
	}
	rows, _ := s.ListProxies()
	if len(rows) != 0 {
		t.Fatal("partial batch committed")
	}
	if err = s.ImportProxies([]string{"http://host:8080", "socks5://host:1080", "http://host:8080/"}); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListProxies()
	if len(rows) != 2 {
		t.Fatal("duplicates", len(rows))
	}
	id, err := s.CreateAccount(&Account{Name: "fixture", State: "registered"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ChangeAccountProxy(id, rows[0].ID) == nil {
		t.Fatal("active account changed")
	}
	if err = s.SetAccountsPaused([]int64{id}, true); err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeAccountProxy(id, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeAccountProxy(id, rows[1].ID); err != nil {
		t.Fatal(err)
	}
	p, _ := s.AccountProxy(id)
	if p.ID != rows[1].ID {
		t.Fatal("binding not changed")
	}
	if _, err = s.CreateJob(id, "check", sql.NullInt64{}); err != nil {
		t.Fatal(err)
	}
	if s.ChangeAccountProxy(id, rows[0].ID) == nil {
		t.Fatal("queued task changed exit")
	}
}

func TestClearProxyBinding(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db)
	id, err := s.CreateAccount(&Account{Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := s.AddProxy(Proxy{URL: "http://127.0.0.1:9"})
	if err != nil {
		t.Fatal(err)
	}
	s.BindAccountProxy(id, pid)
	s.SetAccountsPaused([]int64{id}, true)
	if err = s.ChangeAccountProxy(id, 0); err != nil {
		t.Fatal(err)
	}
	p, err := s.AccountProxy(id)
	if err != nil || p != nil {
		t.Fatal("proxy binding not cleared")
	}
}
