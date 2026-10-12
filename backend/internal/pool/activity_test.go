package pool

import (
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
	"time"
)

func TestScheduledChatHealthyFreeOnly(t *testing.T) {
	now := time.Now()
	a := freeFixture(now)
	p := policy()
	p.TargetFree = 0
	p.ChatInterval = 24 * time.Hour
	p.ChatPrompts = []string{"hello"}
	_, action, err := p.Decide([]store.MaintenanceAccount{a}, now)
	if err != nil || action == nil || action.Kind != "warmup" {
		t.Fatal(action, err)
	}
	a.Jobs["warmup"] = store.Job{ID: 4, State: "ok", FinishedAt: now.Add(-time.Hour).Format("2006-01-02 15:04:05")}
	_, action, err = p.Decide([]store.MaintenanceAccount{a}, now)
	if err != nil || action != nil {
		t.Fatal("chat duplicated", action, err)
	}
	a.Jobs["warmup"] = store.Job{ID: 4, State: "failed", Reason: "reply-not-confirmed"}
	_, action, err = p.Decide([]store.MaintenanceAccount{a}, now)
	if err != nil || action != nil {
		t.Fatal("uncertain send replayed", action, err)
	}
	a.AccessToken = "oauth"
	_, action, err = p.Decide([]store.MaintenanceAccount{a}, now)
	if err != nil || action != nil {
		t.Fatal("OAuth account selected", action, err)
	}
}
func TestRetryPersistsAndBacksOff(t *testing.T) {
	st := maintenanceStore(t)
	id, err := st.CreateAccount(&store.Account{Name: "retry", State: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err := st.ScheduleAccountRetry(id, "register", "blocked:email-box-missing", time.Hour); err != nil {
			t.Fatal(err)
		}
		var due int64
		var failures int
		if err := st.DB().QueryRow("SELECT due_at,failures FROM account_retries WHERE account_id=?", id).Scan(&due, &failures); err != nil {
			t.Fatal(err)
		}
		expected := time.Now().Add(time.Duration(1<<n) * time.Hour).Unix()
		if failures != n+1 || due < expected-2 || due > expected+2 {
			t.Fatal(due, failures)
		}
	}
}

func TestCompletedChatProvesOnboardingWithoutRepeatingIt(t *testing.T) {
	now := time.Now()
	a := freeFixture(now)
	delete(a.Jobs, "onboard")
	a.Jobs["warmup"] = store.Job{ID: a.Jobs["register"].ID + 1, State: "ok", FinishedAt: now.Format("2006-01-02 15:04:05")}
	p := policy()
	p.TargetFree = 0
	p.ChatInterval = 24 * time.Hour
	inventory, action, err := p.Decide([]store.MaintenanceAccount{a}, now)
	if err != nil || action != nil || inventory.Free != 1 {
		t.Fatal("completed chat must not repeat onboarding or message", inventory, action, err)
	}
}

func TestFailedChatResumesAfterHealthyCheckAndInterval(t *testing.T) {
	now := time.Now()
	a := freeFixture(now)
	p := policy()
	p.TargetFree = 0
	p.ChatInterval = 24 * time.Hour
	p.ChatPrompts = []string{"hello"}
	a.Jobs["warmup"] = store.Job{ID: 4, State: "failed", FinishedAt: now.Add(-25 * time.Hour).Format("2006-01-02 15:04:05")}
	check := a.Jobs["check"]
	check.ID = 5
	a.Jobs["check"] = check
	_, action, err := p.Decide([]store.MaintenanceAccount{a}, now)
	if err != nil || action == nil || action.Kind != "warmup" {
		t.Fatalf("failed chat never resumes: %+v %v", action, err)
	}
	j := a.Jobs["warmup"]
	j.FinishedAt = now.Add(-time.Hour).Format("2006-01-02 15:04:05")
	a.Jobs["warmup"] = j
	_, action, err = p.Decide([]store.MaintenanceAccount{a}, now)
	if err != nil || action != nil {
		t.Fatal("chat repeated before interval", action, err)
	}
}
