package api

import (
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
	"time"
)

func TestMailboxFailurePresentation(t *testing.T) {
	now := time.Now()
	a := store.MaintenanceAccount{Account: store.Account{State: "mailbox_create_failed"}, RetryStep: "register", RetryAt: now.Add(time.Minute).Unix()}
	status, detail := accountStatus(a)
	if status != "abnormal" || detail != "邮箱创建失败" {
		t.Fatalf("%s %s", status, detail)
	}
	if got := accountSchedule(a, false, now, 0, 0, time.Hour, 0).Label; got != "等待冷却" {
		t.Fatal(got)
	}
}
