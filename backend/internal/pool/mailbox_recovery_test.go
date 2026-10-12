package pool

import (
	"database/sql"
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
	"time"
)

func TestMissingMailboxDoesNotCoolDown(t *testing.T) {
	a := &store.Account{State: "login_required"}
	if RetryableFailure("register", "blocked:mailbox-mailbox:address-not-found", a) {
		t.Fatal("missing mailbox must not retry on a timer")
	}
	if !RetryableFailure("register", "blocked:mailbox-mailbox:network: timeout", a) {
		t.Fatal("temporary network failure must retry")
	}
}
func TestRecoveryStates(t *testing.T) {
	for _, tc := range []struct{ state, want string }{
		{"login_required", "register"}, {"mailbox_missing", ""}, {"mailbox_config_error", ""},
	} {
		a := store.MaintenanceAccount{Account: store.Account{State: tc.state}, Jobs: map[string]store.Job{}}
		if got := nextMaintenanceStep(a, time.Now(), time.Hour); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.state, got, tc.want)
		}
	}
}

func TestMailboxBlockClearsRetryAndExplicitResume(t *testing.T) {
	st := maintenanceStore(t)
	id, err := st.CreateAccount(&store.Account{Name: "existing", State: "cooldown"})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ScheduleAccountRetry(id, "register", "old temporary error", time.Hour); err != nil {
		t.Fatal(err)
	}
	jid, err := st.CreateJob(id, "register", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.GetAccountByID(id)
	if err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{Store: st, Maintenance: &Maintenance{}}
	if err = s.finish(&store.Job{ID: jid, Kind: "register"}, a, "blocked", "blocked:mailbox-mailbox:address-not-found", "", ""); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAccountByID(id)
	due, err := st.AccountRetryTime(id)
	if err != nil || due != 0 || a.State != "mailbox_missing" {
		t.Fatal(a.State, due, err)
	}
	if err = st.SetAccountsPaused([]int64{id}, false); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAccountByID(id)
	if a.State != "login_required" {
		t.Fatal(a.State)
	}
}
