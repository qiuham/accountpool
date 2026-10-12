package store

import "fmt"

// CommitOutcome writes credentials, lifecycle state and its event atomically.
func (s *Store) CommitOutcome(id int64, expected, to, kind, detail string, fields map[string]any) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actual string
	if err := tx.QueryRow(`SELECT state FROM accounts WHERE id=?`, id).Scan(&actual); err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("account state changed during execution")
	}
	if err := updateFields(tx, id, fields); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE accounts SET state=?,updated_at=? WHERE id=?`, to, now(), id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO events(account_id,at,kind,from_state,to_state,detail) VALUES(?,?,?,?,?,?)`, id, now(), kind, actual, to, detail); err != nil {
		return err
	}
	return tx.Commit()
}

// StartManualJob makes native/manual operations recoverable after process death.
func (s *Store) StartManualJob(accountID int64, kind string) (int64, error) {
	result, err := s.db.Exec(`INSERT INTO jobs(account_id,kind,state,attempts,max_attempts,created_at,started_at) VALUES(?,?,'running',1,1,?,?)`, accountID, kind, now(), now())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}
