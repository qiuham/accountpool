package pool

import (
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
	"time"
)

func TestRecoveryFlowMatrix(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name, state, session, retry  string
		paused, busy, oauth, retired bool
		due                          int64
		want                         string
	}{
		{name: "new", state: "pending", want: "register"},
		{name: "expired-without-history", state: "session_expired", want: "register"},
		{name: "paused", state: "pending", paused: true},
		{name: "running", state: "registering", busy: true},
		{name: "cooldown", state: "cooldown", retry: "register", due: now.Add(time.Hour).Unix()},
		{name: "due", state: "cooldown", retry: "register", due: now.Add(-time.Minute).Unix(), want: "register"},
		{name: "oauth", state: "registered", oauth: true},
		{name: "retired", state: "registered", retired: true},
		{name: "burned", state: "burned"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := store.MaintenanceAccount{Account: store.Account{State: tc.state, SessionKey: tc.session}, Paused: tc.paused, Busy: tc.busy, RetryStep: tc.retry, RetryAt: tc.due, Jobs: map[string]store.Job{}}
			if tc.oauth {
				a.AccessToken = "fixture"
			}
			if tc.retired {
				a.RetiredAt = "2026-10-05"
			}
			if got := nextMaintenanceStep(a, now, time.Hour); got != tc.want {
				t.Fatal(got, tc.want)
			}
		})
	}
}
