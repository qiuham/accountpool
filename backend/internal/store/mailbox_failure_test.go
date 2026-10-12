package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMailboxFailureDoesNotConsumeRegistrationGap(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "pool.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.Exec("UPDATE supply_settings SET value=json_set(value,'$.registrationGapMinutes',60)")
	s := New(db)
	id, e := s.CreateAccount(&Account{Name: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	first, e := s.CreateJob(id, "register", sql.NullInt64{})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ClaimJob(); e != nil {
		t.Fatal(e)
	}
	if e = s.FinishJob(first, "blocked", "mailbox-create-failed", "fixture timeout", ""); e != nil {
		t.Fatal(e)
	}
	s.CreateJob(id, "register", sql.NullInt64{})
	j, e := s.ClaimJob()
	if e != nil {
		t.Fatal(e)
	}
	if j == nil {
		t.Fatal("mailbox failure incorrectly consumes registration cooldown")
	}
}

func TestMailboxRetryDoesNotIncreaseRegistrationBackoff(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "pool.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := New(db)
	id, e := s.CreateAccount(&Account{Name: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if e = s.ScheduleMailboxRetry(id, "mailbox-create-failed"); e != nil {
			t.Fatal(e)
		}
	}
	var failures int
	if e = db.QueryRow("select failures from account_retries where account_id=?", id).Scan(&failures); e != nil {
		t.Fatal(e)
	}
	if failures != 0 {
		t.Fatalf("registration failures=%d", failures)
	}
}
