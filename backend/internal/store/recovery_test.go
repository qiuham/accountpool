package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPauseSurvivesDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := New(db).PauseScheduler("fixture-pause", time.Time{}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := New(db)
	reason, err := st.SchedulerPause()
	if err != nil || reason != "fixture-pause" {
		t.Fatalf("pause lost: %q %v", reason, err)
	}
	if err := st.ResumeScheduler(); err != nil {
		t.Fatal(err)
	}
	reason, err = st.SchedulerPause()
	if err != nil || reason != "" {
		t.Fatal("explicit resume failed")
	}
	if err := st.PauseScheduler("expired", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	reason, err = st.SchedulerPause()
	if err != nil || reason != "" {
		t.Fatal("expired backoff remained active")
	}
}
func TestWorkerLeaseRejectsSecondOwner(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := New(db)
	unlock, err := st.LockWorker()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if second, err := st.LockWorker(); err == nil {
		second()
		t.Fatal("two worker owners accepted")
	}
}
