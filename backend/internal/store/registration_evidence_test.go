package store

import (
	"path/filepath"
	"testing"
)

func TestRegistrationEvidenceIndependentOfState(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db)
	id, err := s.CreateAccount(&Account{Name: "restored", State: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.RegistrationEvidence(id)
	if err != nil || got != "unknown" {
		t.Fatal(got, err)
	}
	s.AddEvent(id, "restore", "", "pending", "metadata only")
	got, _ = s.RegistrationEvidence(id)
	if got != "unknown" {
		t.Fatal("restore is not registration evidence")
	}
	s.AddEvent(id, "registration-confirmed", "", "", "historical successful organization response")
	for _, state := range []string{"pending", "cooldown", "mailbox_missing", "login_required"} {
		if err := s.UpdateState(id, state, "test", ""); err != nil {
			t.Fatal(err)
		}
		got, err = s.RegistrationEvidence(id)
		if err != nil || got != "confirmed" {
			t.Fatal(state, got, err)
		}
	}
}
