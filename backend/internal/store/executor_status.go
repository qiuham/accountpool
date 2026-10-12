package store

import (
	"context"
	"time"
)

func (s *Store) ExecutorHeartbeat(now int64) error {
	_, err := s.db.Exec("INSERT INTO executor_heartbeat(id,updated_at) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at", now)
	return err
}
func (s *Store) KeepExecutorHeartbeat(ctx context.Context) {
	_ = s.ExecutorHeartbeat(time.Now().Unix())
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer s.ExecutorHeartbeat(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.ExecutorHeartbeat(time.Now().Unix())
		}
	}
}
func (s *Store) ExecutorStatus() (map[string]any, error) {
	var at int64
	if err := s.db.QueryRow("SELECT COALESCE((SELECT updated_at FROM executor_heartbeat WHERE id=1),0)").Scan(&at); err != nil {
		return nil, err
	}
	var running int
	if err := s.db.QueryRow("SELECT count(*) FROM jobs WHERE state='running'").Scan(&running); err != nil {
		return nil, err
	}
	reason, err := s.SchedulerPause()
	return map[string]any{"online": at > 0 && time.Now().Unix()-at < 45, "heartbeatAt": at, "running": running, "paused": reason != "", "mode": "独立 Docker 执行器"}, err
}
