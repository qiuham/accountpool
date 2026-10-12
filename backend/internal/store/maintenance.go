package store

import (
	"fmt"
	"path/filepath"
	"strings"
)

// MaintenanceAccount contains persisted evidence needed by the controller.
// Jobs includes the latest attempt at each step, including composite manual jobs.
type MaintenanceAccount struct {
	Account
	Jobs                 map[string]Job
	Paused               bool
	Busy                 bool
	LastHealthyCheckID   int64
	LastCompletedCheck   Job // Latest terminal check; queued/running attempts never replace health evidence.
	RetryStep            string
	RetryAt              int64
	AutomaticallyCreated bool
}
type MaintenanceAction struct {
	AccountID  int64
	Kind       string
	NewAccount *Account
}

// ReconcileMaintenance reads and writes one consistent snapshot. The caller
// holds the worker lease, so recovery and execution cannot race this decision.
func (s *Store) ReconcileMaintenance(plan func([]MaintenanceAccount) (*MaintenanceAction, error)) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id,name,state,COALESCE(plan,''),COALESCE(session_key,''),COALESCE(access_token,''),COALESCE(refresh_token,''),COALESCE(subscribed_at,''),COALESCE(retired_at,''),created_at,COALESCE(last_check_at,''), EXISTS(SELECT 1 FROM events WHERE account_id=accounts.id AND kind='create' AND detail='free-pool maintenance') , EXISTS(SELECT 1 FROM account_maintenance_pauses WHERE account_id=accounts.id) FROM accounts ORDER BY id`)
	if err != nil {
		return err
	}
	var accounts []MaintenanceAccount
	indexes := map[int64]int{}
	for rows.Next() {
		a := MaintenanceAccount{Jobs: map[string]Job{}}
		if err := rows.Scan(&a.ID, &a.Name, &a.State, &a.Plan, &a.SessionKey, &a.AccessToken, &a.RefreshToken, &a.SubscribedAt, &a.RetiredAt, &a.CreatedAt, &a.LastCheckAt, &a.AutomaticallyCreated, &a.Paused); err != nil {
			rows.Close()
			return err
		}
		indexes[a.ID] = len(accounts)
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	retryRows, err := tx.Query("SELECT account_id,step,due_at FROM account_retries")
	if err != nil {
		return err
	}
	for retryRows.Next() {
		var id int64
		var step string
		var due int64
		if err := retryRows.Scan(&id, &step, &due); err != nil {
			retryRows.Close()
			return err
		}
		if i, ok := indexes[id]; ok {
			accounts[i].RetryStep = step
			accounts[i].RetryAt = due
		}
	}
	if err := retryRows.Err(); err != nil {
		retryRows.Close()
		return err
	}
	retryRows.Close()
	rows, err = tx.Query(`SELECT id,account_id,kind,state,COALESCE(reason,''),COALESCE(finished_at,'') FROM jobs ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.AccountID, &j.Kind, &j.State, &j.Reason, &j.FinishedAt); err != nil {
			rows.Close()
			return err
		}
		i, ok := indexes[j.AccountID]
		if !ok {
			continue
		}
		if j.State == "pending" || j.State == "running" {
			accounts[i].Busy = true
		}
		for _, kind := range strings.Split(j.Kind, ",") {
			accounts[i].Jobs[kind] = j
			if kind == "check" && (j.State == "ok" || j.State == "failed" || j.State == "blocked") {
				accounts[i].LastCompletedCheck = j
			}
			if kind == "check" && j.State == "ok" {
				accounts[i].LastHealthyCheckID = j.ID
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	action, err := plan(accounts)
	if err != nil {
		return err
	}
	if action == nil {
		return tx.Commit()
	}
	id := action.AccountID
	if a := action.NewAccount; a != nil {
		var digits int
		if err := tx.QueryRow("SELECT COALESCE(json_extract(value,'$.sequenceDigits'),3) FROM supply_settings WHERE id=1").Scan(&digits); err != nil {
			return err
		}
		prefix := strings.ToLower(a.Name)
		for {
			var number int64
			if err := tx.QueryRow("UPDATE mailbox_sequence SET last_value=last_value+1 WHERE id=1 RETURNING last_value").Scan(&number); err != nil {
				return err
			}
			name := fmt.Sprintf("%s%0*d", prefix, digits, number)
			var exists bool
			if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM accounts WHERE lower(name)=? OR lower(substr(email,1,instr(email,'@')-1))=?) OR EXISTS(SELECT 1 FROM tombstones WHERE kind='email' AND lower(substr(value,1,instr(value,'@')-1))=?)", name, name, name).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			a.Name = name
			a.BundlePath = filepath.Join("bundles", name)
			break
		}

		result, err := tx.Exec(`INSERT INTO accounts(name,bundle_path,state,created_at,updated_at) VALUES(?,?,'pending',?,?)`, a.Name, a.BundlePath, now(), now())
		if err != nil {
			return err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO events(account_id,at,kind,from_state,to_state,detail) VALUES(?,?,'create','','pending','free-pool maintenance')`, id, now()); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO jobs(account_id,kind,state,created_at) SELECT ?,?,'pending',? WHERE NOT EXISTS(SELECT 1 FROM jobs WHERE account_id=? AND state IN ('pending','running'))`, id, action.Kind, now(), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SessionRecoveryAllowed permits one login after a previously healthy session.
// A fresh registration followed by an expired check is not retried in a loop.
func (s *Store) SessionRecoveryAllowed(accountID int64) (bool, error) {
	var healthy, registration int64
	err := s.db.QueryRow(`SELECT
 COALESCE(MAX(CASE WHEN instr(','||kind||',',',check,')>0 AND state='ok' THEN id END),0),
 COALESCE(MAX(CASE WHEN instr(','||kind||',',',register,')>0 THEN id END),0)
 FROM jobs WHERE account_id=?`, accountID).Scan(&healthy, &registration)
	return registration == 0 || healthy >= registration, err
}
