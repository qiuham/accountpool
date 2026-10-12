package pool

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
	"time"
)

type unavailableMailbox struct{}

func (unavailableMailbox) EnsureMailbox(*store.Store, *store.Account) (string, error) {
	return "", errors.New("fixture connection reset")
}
func TestMailboxFailureIndependentRetry(t *testing.T) {
	st, id := reliabilityStore(t)
	st.CreateJob(id, "register", sql.NullInt64{})
	job, e := st.ClaimJob()
	if e != nil {
		t.Fatal(e)
	}
	runner := &captureRunner{}
	m := policy()
	s := &Scheduler{Store: st, Runner: runner, Mail: unavailableMailbox{}, Maintenance: &m, BaseCooldown: 30 * time.Minute}
	_, e = s.runOne(context.Background(), job)
	if e != nil {
		t.Fatal(e)
	}
	a, e := st.GetAccountByID(id)
	if e != nil {
		t.Fatal(e)
	}
	if a.State != "mailbox_create_failed" || runner.calls != 0 {
		t.Fatalf("state=%s browser calls=%d", a.State, runner.calls)
	}
	due, e := st.AccountRetryTime(id)
	if e != nil {
		t.Fatal(e)
	}
	if d := due - time.Now().Unix(); d < 55 || d > 60 {
		t.Fatalf("mail retry delay=%d", d)
	}
	if !CanTransition(State(a.State), Registering) {
		t.Fatal("retry cannot proceed to registration")
	}
	if RetryableFailure("register", "mailbox-create-failed", a) {
		t.Fatal("mailbox consumed registration backoff")
	}
}
