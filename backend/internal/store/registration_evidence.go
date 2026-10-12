package store

// RegistrationEvidence reports persisted observations, never scheduler intent.
func (s *Store) RegistrationEvidence(id int64) (string, error) {
	var confirmed bool
	err := s.db.QueryRow(`SELECT EXISTS(
 SELECT 1 FROM accounts WHERE id=? AND (
 COALESCE(org_id,'')!='' OR COALESCE(registered_at,'')!='' OR
 EXISTS(SELECT 1 FROM events WHERE account_id=accounts.id AND
 (kind='registration-confirmed' OR (kind='register' AND json_valid(detail) AND json_extract(detail,'$.status')='ok')))
 ))`, id).Scan(&confirmed)
	if err != nil {
		return "", err
	}
	if confirmed {
		return "confirmed", nil
	}
	return "unknown", nil
}
