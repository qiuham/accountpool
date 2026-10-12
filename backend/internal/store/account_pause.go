package store

import "fmt"

// SetAccountsPaused atomically changes maintenance intent; running tasks finish.
func (s *Store) SetAccountsPaused(ids []int64, paused bool) error {
	if len(ids) == 0 || len(ids) > 500 {
		return fmt.Errorf("请选择 1–500 个账号")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return fmt.Errorf("账号选择无效")
		}
		seen[id] = true
		var exists int
		if err = tx.QueryRow("SELECT COUNT(*) FROM accounts WHERE id=? AND retired_at IS NULL AND state!='burned'", id).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return fmt.Errorf("账号不存在、已退役或已失效，请刷新列表")
		}
		if paused {
			_, err = tx.Exec("INSERT OR IGNORE INTO account_maintenance_pauses(account_id,paused_at) VALUES(?,?)", id, now())
		} else {
			_, err = tx.Exec("DELETE FROM account_maintenance_pauses WHERE account_id=?", id)
			if err == nil {
				_, err = tx.Exec("UPDATE accounts SET state='login_required',updated_at=? WHERE id=? AND state IN ('mailbox_missing','mailbox_config_error')", now(), id)
			}
			if err == nil {
				// A metadata-only restored account has no previous task to resume.
				// Start its existing-email login flow only on explicit resume.
				_, err = tx.Exec(`UPDATE accounts SET state=CASE WHEN COALESCE(session_key,'')='' THEN 'login_required' ELSE 'registered' END,updated_at=? WHERE id=? AND state='observing' AND COALESCE(access_token,'')='' AND COALESCE(refresh_token,'')='' AND retired_at IS NULL AND EXISTS(SELECT 1 FROM events WHERE account_id=accounts.id AND kind='restore') AND NOT EXISTS(SELECT 1 FROM jobs WHERE account_id=accounts.id)`, now(), id)
			}
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) AccountMaintenancePaused(id int64) (bool, error) {
	var paused bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM account_maintenance_pauses WHERE account_id=?)", id).Scan(&paused)
	return paused, err
}
