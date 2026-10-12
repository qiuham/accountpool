package api

import (
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
	"time"
)

func TestAccountStatusUsesEvidence(t *testing.T) {
	now := time.Now()
	fresh := func() store.MaintenanceAccount {
		return store.MaintenanceAccount{LastCompletedCheck: store.Job{ID: 3, State: "ok"}, Account: store.Account{State: "aging", SessionKey: "fixture"}, Jobs: map[string]store.Job{"register": {ID: 1, State: "ok"}, "onboard": {ID: 2, State: "ok"}, "check": {ID: 3, State: "ok", FinishedAt: now.Add(-time.Minute).Format("2006-01-02 15:04:05")}}}
	}
	cases := []struct {
		name, want string
		change     func(*store.MaintenanceAccount)
	}{
		{"normal", "normal", func(a *store.MaintenanceAccount) {}},
		{"legacy aging not enough", "initializing", func(a *store.MaintenanceAccount) { a.Jobs = map[string]store.Job{} }},
		{"expired", "normal", func(a *store.MaintenanceAccount) {
			a.Jobs["check"] = store.Job{ID: 3, State: "ok", FinishedAt: now.Add(-2 * time.Hour).Format("2006-01-02 15:04:05")}
		}},
		{"session missing", "login", func(a *store.MaintenanceAccount) { a.SessionKey = "" }},
		{"paused healthy", "normal", func(a *store.MaintenanceAccount) { a.Paused = true }},
		{"mail missing", "abnormal", func(a *store.MaintenanceAccount) { a.State = "mailbox_missing" }},
		{"disabled", "disabled", func(a *store.MaintenanceAccount) { a.RetiredAt = "fixture" }},
		{"new login invalidates old check", "initializing", func(a *store.MaintenanceAccount) { a.Jobs["register"] = store.Job{ID: 4, State: "ok"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := fresh()
			tc.change(&a)
			got, _ := accountStatus(a)
			if got != tc.want {
				t.Fatalf("%s != %s", got, tc.want)
			}
		})
	}
	a := fresh()
	a.RetryStep = "register"
	a.RetryAt = now.Add(time.Minute).Unix()
	if accountSchedule(a, false, now, 0, 0, time.Hour, 0).Label != "等待冷却" {
		t.Fatal("missing cooldown")
	}
	a.Paused = true
	if accountSchedule(a, false, now, 0, 0, time.Hour, 0).Label != "已暂停" {
		t.Fatal("missing pause")
	}
	a.Jobs["check"] = store.Job{State: "running"}
	if accountSchedule(a, false, now, 0, 0, time.Hour, 0).Label != "执行中" {
		t.Fatal("pause must not hide running action")
	}
}
