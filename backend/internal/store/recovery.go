package store

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// LockWorker serializes recovery and job claiming for this database.
func (s *Store) LockWorker() (func(), error) {
	var path string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("worker requires a file-backed database")
	}
	f, err := os.OpenFile(path+".worker.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("worker already running: %w", err)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func (s *Store) PauseScheduler(reason string, until time.Time) error {
	var deadline int64
	if !until.IsZero() {
		deadline = until.Unix()
	}
	_, err := s.db.Exec(`INSERT INTO scheduler_control(id,reason,until_unix) VALUES(1,?,?)
 ON CONFLICT(id) DO UPDATE SET reason=excluded.reason,until_unix=excluded.until_unix`, reason, deadline)
	return err
}
func (s *Store) SchedulerPause() (string, error) {
	var reason string
	err := s.db.QueryRow(`SELECT COALESCE((SELECT reason FROM scheduler_control
 WHERE id=1 AND (until_unix=0 OR until_unix>?)),'')`, time.Now().Unix()).Scan(&reason)
	return reason, err
}
func (s *Store) ResumeScheduler() error {
	_, err := s.db.Exec("DELETE FROM scheduler_control WHERE id=1")
	return err
}

// RecoverInterruptedJobs never replays an uncertain upstream submission.
// Call only after obtaining the worker lease and cleaning up its executor.
func (s *Store) RecoverInterruptedJobs() (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE proxies SET state='available' WHERE state='in_use'
 AND id IN (SELECT proxy_id FROM jobs WHERE state='running')`)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE jobs SET state='blocked',reason='interrupted-unknown-outcome',
 detail='Executor interrupted; inspect prior artifacts before retrying',finished_at=? WHERE state='running'`, now())
	if err != nil {
		return 0, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count > 0 {
		_, err = tx.Exec(`INSERT INTO scheduler_control(id,reason,until_unix)
 VALUES(1,'interrupted-unknown-outcome',0) ON CONFLICT(id) DO UPDATE SET reason=excluded.reason,until_unix=0`)
		if err != nil {
			return 0, err
		}
	}
	return count, tx.Commit()
}
