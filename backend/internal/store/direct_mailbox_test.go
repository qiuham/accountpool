package store

import (
	"path/filepath"
	"testing"
)

func TestDirectMailboxSchema(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='account_mailboxes'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("forwarding table still exists")
	}
	st := New(db)
	id, err := st.CreateAccount(&Account{Name: "direct", Email: "direct@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.GetAccountByID(id)
	if err != nil || a.Email != "direct@example.test" {
		t.Fatal(a, err)
	}
}
