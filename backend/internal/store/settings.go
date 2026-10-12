package store

import (
	"encoding/json"
	"fmt"
)

// RuntimeSettings contains operational limits, not credentials or deployment paths.
type RuntimeSettings struct {
	GapSeconds         int `json:"gapSeconds"`
	CooldownSeconds    int `json:"cooldownSeconds"`
	FailureLimit       int `json:"failureLimit"`
	TaskTimeoutSeconds int `json:"taskTimeoutSeconds"`
}

func DefaultRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{60, 1800, 3, 1800}
}
func (v RuntimeSettings) Validate() error {
	if v.GapSeconds < 60 || v.GapSeconds > 86400 || v.CooldownSeconds < 60 || v.CooldownSeconds > 604800 || v.FailureLimit < 1 || v.FailureLimit > 100 || v.TaskTimeoutSeconds < 60 || v.TaskTimeoutSeconds > 86400 {
		return fmt.Errorf("间隔需为 60–86400 秒，冷却为 60–604800 秒，失败阈值为 1–100，任务超时为 60–86400 秒")
	}
	return nil
}
func (s *Store) RuntimeSettings() (RuntimeSettings, error) {
	var raw string
	err := s.db.QueryRow("SELECT value FROM runtime_settings WHERE id=1").Scan(&raw)
	if err != nil {
		return RuntimeSettings{}, err
	}
	var v RuntimeSettings
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return v, err
	}
	return v, v.Validate()
}
func (s *Store) SaveRuntimeSettings(v RuntimeSettings) error {
	if err := v.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE runtime_settings SET value=? WHERE id=1", string(raw))
	return err
}
