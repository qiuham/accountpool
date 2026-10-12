package store

import (
	"path/filepath"
	"testing"
)

func TestExplicitResumeStartsRestoredAccountOnly(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db)
	for _, restored := range []bool{true, false} {
		name := "ordinary"
		if restored {
			name = "restored"
		}
		id, err := s.CreateAccount(&Account{Name: name, State: "observing"})
		if err != nil {
			t.Fatal(err)
		}
		if restored {
			if _, err = db.Exec("INSERT INTO events(account_id,at,kind) VALUES(?,'2026-10-05','restore')", id); err != nil {
				t.Fatal(err)
			}
		}
		if err = s.SetAccountsPaused([]int64{id}, true); err != nil {
			t.Fatal(err)
		}
		if err = s.SetAccountsPaused([]int64{id}, false); err != nil {
			t.Fatal(err)
		}
		a, err := s.GetAccount(name)
		if err != nil {
			t.Fatal(err)
		}
		want := "observing"
		if restored {
			want = "login_required"
		}
		if a.State != want {
			t.Fatal(a.State, want)
		}
	}
}
