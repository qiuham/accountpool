package store

import (
	"path/filepath"
	"testing"
)

func TestMaintenancePausePersistsAndSkipsQueuedJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st := New(db)
	a, err := st.CreateAccount(&Account{Name: "one", State: "registered"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateAccount(&Account{Name: "two", State: "registered"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a, b} {
		if _, err = db.Exec("INSERT INTO jobs(account_id,kind,state,created_at) VALUES(?,'check','pending','2026-10-05')", id); err != nil {
			t.Fatal(err)
		}
	}
	if err = st.SetAccountsPaused([]int64{a, 999999}, true); err == nil {
		t.Fatal("invalid batch accepted")
	}
	if paused, _ := st.AccountMaintenancePaused(a); paused {
		t.Fatal("partial mutation")
	}
	if err = st.SetAccountsPaused([]int64{a}, true); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st = New(db)
	if paused, err := st.AccountMaintenancePaused(a); err != nil || !paused {
		t.Fatal(paused, err)
	}
	job, err := st.ClaimJob()
	if err != nil || job == nil || job.AccountID != b {
		t.Fatal(job, err)
	}
	if err = st.SetAccountsPaused([]int64{b}, true); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.QueryRow("SELECT state FROM jobs WHERE id=?", job.ID).Scan(&state); err != nil || state != "running" {
		t.Fatal(state, err)
	}
	if job, err = st.ClaimJob(); err != nil || job != nil {
		t.Fatal(job, err)
	}
	if err = st.SetAccountsPaused([]int64{a}, false); err != nil {
		t.Fatal(err)
	}
	if job, err = st.ClaimJob(); err != nil || job == nil || job.AccountID != a {
		t.Fatal(job, err)
	}
}
