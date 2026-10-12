package api

import (
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
	"time"
)

func TestIndependentActivityTimers(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	a := store.MaintenanceAccount{Account: store.Account{State: "aging", SessionKey: "fixture", Plan: "claude_free"}, Jobs: map[string]store.Job{
		"register": {ID: 1, State: "ok"}, "onboard": {ID: 2, State: "ok"},
		"check":  {ID: 4, State: "ok", FinishedAt: now.Add(-30 * time.Minute).Format("2006-01-02 15:04:05")},
		"warmup": {ID: 3, State: "ok", FinishedAt: now.Add(-time.Hour).Format("2006-01-02 15:04:05")},
	}}
	s := accountSchedule(a, false, now, 0, 0, time.Hour, 24*time.Hour)
	if s.Check.ReadyAt != now.Add(30*time.Minute).Unix() || s.Chat.ReadyAt != now.Add(23*time.Hour).Unix() {
		t.Fatalf("%+v", s)
	}
	a.RetryStep = "warmup"
	a.RetryAt = now.Add(5 * time.Minute).Unix()
	s = accountSchedule(a, false, now, 0, 0, time.Hour, 24*time.Hour)
	if s.Chat.Label != "重试" || s.Chat.ReadyAt != a.RetryAt || s.Check.ReadyAt == 0 {
		t.Fatalf("%+v", s)
	}
	s = accountSchedule(a, false, now, 0, 0, time.Hour, 0)
	if s.Chat.ReadyAt != 0 || s.Chat.Label != "未启用" {
		t.Fatal(s.Chat)
	}
	a.Paused = true
	s = accountSchedule(a, false, now, 0, 0, time.Hour, 24*time.Hour)
	if s.Check.ReadyAt != 0 || s.Chat.ReadyAt != 0 || s.Chat.Label != "已暂停" {
		t.Fatalf("%+v", s)
	}
}

func TestFailedChatTimerAfterRecovery(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	a := store.MaintenanceAccount{Account: store.Account{State: "aging", SessionKey: "fixture", Plan: "claude_free"}, Jobs: map[string]store.Job{
		"register": {ID: 1, State: "ok"}, "onboard": {ID: 2, State: "ok"},
		"warmup": {ID: 3, State: "failed", FinishedAt: now.Add(-time.Hour).Format("2006-01-02 15:04:05")},
		"check":  {ID: 4, State: "ok", FinishedAt: now.Format("2006-01-02 15:04:05")},
	}}
	s := accountSchedule(a, false, now, 0, 0, time.Hour, 24*time.Hour)
	if s.Chat.ReadyAt != now.Add(23*time.Hour).Unix() || s.Chat.Label != "周期" {
		t.Fatalf("recovered account still blocked: %+v", s.Chat)
	}
}
