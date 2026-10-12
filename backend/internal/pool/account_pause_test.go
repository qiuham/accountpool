package pool

import (
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
	"time"
)

func TestPausedAccountReservesCapacityWithoutScheduling(t *testing.T) {
	now := time.Now()
	a := freeFixture(now)
	a.Paused = true
	a.State = "session_expired"
	a.RetryStep = "register"
	a.RetryAt = now.Add(-time.Hour).Unix()
	p := policy()
	p.TargetFree = 1
	p.ChatInterval = time.Hour
	inv, action, err := p.Decide([]store.MaintenanceAccount{a}, now)
	if err != nil || action != nil || inv.Reserved != 1 {
		t.Fatal(inv, action, err)
	}
}
