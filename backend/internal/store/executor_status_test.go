package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestExecutorHeartbeatStatus(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "pool.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := New(db)
	for _, tc := range []struct {
		at   int64
		want bool
	}{{0, false}, {time.Now().Unix(), true}, {time.Now().Unix() - 60, false}} {
		if e := s.ExecutorHeartbeat(tc.at); e != nil {
			t.Fatal(e)
		}
		got, e := s.ExecutorStatus()
		if e != nil || got["online"] != tc.want {
			t.Fatal(got, e)
		}
	}
}
