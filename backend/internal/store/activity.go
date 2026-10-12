package store

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

type ActivitySettings struct {
	Enabled       bool     `json:"enabled"`
	IntervalHours int      `json:"intervalHours"`
	Prompts       []string `json:"prompts"`
}

func validatePrompts(prompts []string) error {
	if len(prompts) < 1 || len(prompts) > 100 {
		return fmt.Errorf("请配置 1–100 条对话消息")
	}
	for _, p := range prompts {
		if len(strings.TrimSpace(p)) == 0 || len([]rune(p)) > 2000 {
			return fmt.Errorf("每条消息需为 1–2000 字符")
		}
	}
	return nil
}
func RandomPrompt(prompts []string) (string, error) {
	if err := validatePrompts(prompts); err != nil {
		return "", err
	}
	return prompts[rand.IntN(len(prompts))], nil
}
func (v ActivitySettings) Validate() error {
	if v.IntervalHours < 1 || v.IntervalHours > 720 {
		return fmt.Errorf("网页对话周期需为 1–720 小时")
	}
	return validatePrompts(v.Prompts)
}
func (s *Store) ActivitySettings() (ActivitySettings, error) {
	var raw string
	var v ActivitySettings
	err := s.db.QueryRow("SELECT value FROM activity_settings WHERE id=1").Scan(&raw)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(raw), &v)
	return v, err
}
func (s *Store) SaveActivitySettings(v ActivitySettings) error {
	if err := v.Validate(); err != nil {
		return err
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("UPDATE activity_settings SET value=? WHERE id=1", string(b))
	return e
}
func (s *Store) ScheduleAccountRetry(accountID int64, step, reason string, base time.Duration) error {
	if base < time.Minute {
		base = 30 * time.Minute
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT COALESCE((SELECT failures FROM account_retries WHERE account_id=?),0)", accountID).Scan(&count); err != nil {
		return err
	}
	delay := base
	for i := 0; i < count && delay < 24*time.Hour; i++ {
		delay *= 2
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	_, err = tx.Exec("INSERT INTO account_retries(account_id,step,due_at,failures,reason) VALUES(?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET step=excluded.step,due_at=excluded.due_at,failures=excluded.failures,reason=excluded.reason", accountID, step, time.Now().Add(delay).Unix(), count+1, reason)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO events(account_id,at,kind,from_state,to_state,detail) SELECT id,?,'retry-scheduled',state,'cooldown',? FROM accounts WHERE id=? AND state!='burned' AND retired_at IS NULL", now(), reason, accountID); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE accounts SET state='cooldown',last_error=?,updated_at=? WHERE id=? AND state!='burned' AND retired_at IS NULL", reason, now(), accountID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ClearAccountRetry(accountID int64) error {
	_, err := s.db.Exec("DELETE FROM account_retries WHERE account_id=?", accountID)
	return err
}

func (s *Store) AccountRetryTime(id int64) (int64, error) {
	var due int64
	err := s.db.QueryRow("SELECT COALESCE((SELECT due_at FROM account_retries WHERE account_id=?),0)", id).Scan(&due)
	return due, err
}

// ScheduleMailboxRetry retries provisioning independently of registration backoff.
// A fixed one-minute wait prevents a hot loop without consuming registration attempts.
func (s *Store) ScheduleMailboxRetry(accountID int64, reason string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO account_retries(account_id,step,due_at,failures,reason) VALUES(?,'register',?,0,?) ON CONFLICT(account_id) DO UPDATE SET step=excluded.step,due_at=excluded.due_at,failures=0,reason=excluded.reason", accountID, time.Now().Add(time.Minute).Unix(), reason)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE accounts SET state='mailbox_create_failed',last_error=?,updated_at=? WHERE id=? AND retired_at IS NULL AND state!='burned'", reason, now(), accountID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
