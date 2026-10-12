package api

import (
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
	"time"
)

func TestScheduleShowsGlobalGate(t *testing.T) {
	now := time.Now()
	a := store.MaintenanceAccount{Account: store.Account{State: "registering"}, Busy: true, Jobs: map[string]store.Job{"register": {State: "pending"}}}
	s := accountSchedule(a, false, now, now.Add(10*time.Minute).Unix(), 0, time.Hour, 0)
	if s.Label != "等待冷却" || s.ReadyAt != now.Add(10*time.Minute).Unix() {
		t.Fatalf("%+v", s)
	}
	a.Paused = true
	s = accountSchedule(a, false, now, now.Add(time.Hour).Unix(), 0, time.Hour, 0)
	if s.ReadyAt != 0 || s.Label != "已暂停" {
		t.Fatalf("%+v", s)
	}
}
func TestScheduleUsesLongestGate(t *testing.T) {
	now := time.Now()
	a := store.MaintenanceAccount{Account: store.Account{State: "cooldown"}, RetryStep: "register", RetryAt: now.Add(time.Minute).Unix()}
	s := accountSchedule(a, false, now, now.Add(10*time.Minute).Unix(), now.Add(20*time.Minute).Unix(), time.Hour, 0)
	if s.ReadyAt != now.Add(20*time.Minute).Unix() {
		t.Fatalf("%+v", s)
	}
}
func TestScheduleDoesNotInventQueueETA(t *testing.T) {
	now := time.Now()
	a := store.MaintenanceAccount{Busy: true, Jobs: map[string]store.Job{"check": {State: "pending"}}}
	s := accountSchedule(a, false, now, 0, 0, time.Hour, 0)
	if s.ReadyAt != 0 || s.Label != "排队中" {
		t.Fatalf("%+v", s)
	}
}
func TestScheduleNextCheckAndChat(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	a := store.MaintenanceAccount{Account: store.Account{State: "aging", SessionKey: "fixture", Plan: "claude_free"}, Jobs: map[string]store.Job{
		"register": {ID: 1, State: "ok"}, "onboard": {ID: 2, State: "ok"},
		"check":  {ID: 4, State: "ok", FinishedAt: now.Add(-30 * time.Minute).Format("2006-01-02 15:04:05")},
		"warmup": {ID: 3, State: "ok", FinishedAt: now.Add(-50 * time.Minute).Format("2006-01-02 15:04:05")},
	}}
	s := accountSchedule(a, false, now, 0, 0, time.Hour, time.Hour)
	if s.Label != "等待对话" || s.ReadyAt != now.Add(10*time.Minute).Unix() {
		t.Fatalf("%+v", s)
	}
	s = accountSchedule(a, false, now, 0, 0, time.Hour, 0)
	if s.Label != "等待检查" || s.ReadyAt != now.Add(30*time.Minute).Unix() {
		t.Fatalf("%+v", s)
	}
}
