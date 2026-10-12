package store

import "time"

// RegistrationReadyAt exposes the same persisted platform gate used by ClaimJob.
func (s *Store) RegistrationReadyAt(gapMinutes int) (int64, error) {
	var last string
	err := s.db.QueryRow(`SELECT COALESCE(MAX(COALESCE(finished_at,started_at)),'') FROM jobs WHERE instr(','||kind||',', ',register,')>0 AND started_at IS NOT NULL AND COALESCE(reason,'') NOT IN ('mailbox-create-failed','mailbox-prerequisite-failed')`).Scan(&last)
	if err != nil || last == "" {
		return 0, err
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", last, time.Local)
	if err != nil {
		return 0, err
	}
	return t.Add(time.Duration(gapMinutes) * time.Minute).Unix(), nil
}
