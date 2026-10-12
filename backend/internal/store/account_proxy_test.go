package store

import (
	"path/filepath"
	"testing"
)

func TestAccountProxyBinding(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db)
	id, err := s.CreateAccount(&Account{Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	p1, err := s.EnsureProxy("http://127.0.0.1:9001")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.EnsureProxy("http://127.0.0.1:9002")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.BindAccountProxy(id, p1); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BindAccountProxy(id, p2); err == nil {
		t.Fatal("silent proxy replacement")
	}
	p, err := s.AccountProxy(id)
	if err != nil || p == nil || p.ID != p1 {
		t.Fatal("binding lost")
	}
}
func TestParseProxyURL(t *testing.T) {
	for _, raw := range []string{"http://localhost", "ftp://localhost:21", "http://u:secret@localhost:99999", "http://localhost:80/path"} {
		if _, _, err := ParseProxyURL(raw); err == nil {
			t.Fatal("invalid proxy accepted")
		}
	}
}
