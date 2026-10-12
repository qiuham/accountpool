package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRegistrationGapSkipsOnlyRegistration(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := New(db)
	_, err = db.Exec(`UPDATE supply_settings SET value=json_set(value,'$.registrationGapMinutes',30)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1, 2} {
		_, err = db.Exec("INSERT INTO accounts(id,name,state,created_at,updated_at) VALUES(?,?,'pending',?,?)", id, time.Now().String()+string(rune(id)), now(), now())
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec("INSERT INTO jobs(account_id,kind,state,created_at,started_at,finished_at) VALUES(1,'register','failed',?,?,?)", now(), now(), now())
	if err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"register", "check"} {
		_, err = db.Exec("INSERT INTO jobs(account_id,kind,state,created_at) VALUES(2,?,'pending',?)", kind, now())
		if err != nil {
			t.Fatal(err)
		}
	}
	j, err := st.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if j == nil || j.Kind != "check" {
		t.Fatalf("must skip register and allow check: %+v", j)
	}
	j, err = st.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if j != nil {
		t.Fatal("registration claimed during cooldown")
	}
	_, err = db.Exec("UPDATE jobs SET finished_at=? WHERE account_id=1", time.Now().Add(-31*time.Minute).Format("2006-01-02 15:04:05"))
	if err != nil {
		t.Fatal(err)
	}
	j, err = New(db).ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if j == nil || j.Kind != "register" {
		t.Fatal("registration not released after persisted cooldown")
	}

}
