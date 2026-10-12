package store

import (
	"path/filepath"
	"testing"
)

func TestMailboxSequencePersistentAndNonReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st := New(db)
	create := func(prefix string) string {
		t.Helper()
		a := &Account{Name: prefix}
		if err := st.ReconcileMaintenance(func([]MaintenanceAccount) (*MaintenanceAction, error) {
			return &MaintenanceAction{Kind: "register", NewAccount: a}, nil
		}); err != nil {
			t.Fatal(err)
		}
		return a.Name
	}
	if got := create("Claude"); got != "claude001" {
		t.Fatal(got)
	}
	if _, err := db.Exec("DELETE FROM accounts"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st = New(db)
	if got := create("Claude"); got != "claude002" {
		t.Fatal(got)
	}
	if _, err = st.CreateAccount(&Account{Name: "claude003"}); err != nil {
		t.Fatal(err)
	}
	if got := create("Claude"); got != "claude004" {
		t.Fatal(got)
	}
	if _, err = db.Exec("UPDATE mailbox_sequence SET last_value=999"); err != nil {
		t.Fatal(err)
	}
	if got := create("Claude"); got != "claude1000" {
		t.Fatal(got)
	}
	if _, err = db.Exec("UPDATE supply_settings SET value=json_set(value,'$.sequenceDigits',5)"); err != nil {
		t.Fatal(err)
	}
	if got := create("new-"); got != "new-01001" {
		t.Fatal(got)
	}
}
