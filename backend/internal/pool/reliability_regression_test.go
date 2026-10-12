package pool

import (
	"context"
	"database/sql"
	"github.com/ham/accountpool/backend/internal/store"
	"path/filepath"
	"testing"
)

func reliabilityStore(t *testing.T) (*store.Store, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)
	if _, err := st.AddProxy(store.Proxy{URL: "http://127.0.0.1:9"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateAccount(&store.Account{Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return st, id
}
func TestWorkerRestartRetainsPause(t *testing.T) {
	st, a := reliabilityStore(t)
	for i := 0; i < 2; i++ {
		st.CreateJob(a, "check", sql.NullInt64{})
	}
	first := &resultRunner{result: RunnerResult{Status: "blocked", Reason: "blocked:cf-challenge-human-required"}}
	if _, err := (&Scheduler{Store: st, Runner: first}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := &captureRunner{}
	stats, err := (&Scheduler{Store: st, Runner: next}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Stopped || next.calls != 0 {
		t.Fatal("worker restart bypassed pause")
	}
}
func TestInterruptedJobIsNotLeftRunning(t *testing.T) {
	st, a := reliabilityStore(t)
	st.CreateJob(a, "register", sql.NullInt64{})
	st.ClaimJob()
	next := &captureRunner{}
	stats, err := (&Scheduler{Store: st, Runner: next}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := st.ListJobs(10)
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].State != "blocked" || jobs[0].Reason != "interrupted-unknown-outcome" || !stats.Stopped || next.calls != 0 {
		t.Fatal("interrupted job not isolated for inspection")
	}
}
func TestTransientBackoffPersistsWithAccountProxy(t *testing.T) {
	st, a := reliabilityStore(t)
	st.CreateJob(a, "check", sql.NullInt64{})
	first := &resultRunner{result: RunnerResult{Status: "failed", Reason: "navigation-timeout"}}
	if _, err := (&Scheduler{Store: st, Runner: first}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := &captureRunner{}
	stats, err := (&Scheduler{Store: st, Runner: next}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.calls != 1 || next.calls != 0 || !stats.Stopped {
		t.Fatal("transient backoff lost without proxy")
	}
}

type uncertainFailure struct{}

func (uncertainFailure) Error() string        { return "fixture interrupted after submission" }
func (uncertainFailure) OutcomeUnknown() bool { return true }

type uncertainRunner struct{ calls int }

func (r *uncertainRunner) Run(context.Context, RunnerSpec) (*RunnerResult, error) {
	r.calls++
	return nil, uncertainFailure{}
}
func TestUncertainExecutorDoesNotRetry(t *testing.T) {
	st, a := reliabilityStore(t)
	st.CreateJob(a, "register", sql.NullInt64{}) // use check to avoid mailbox preparation
	st.DB().Exec("UPDATE jobs SET kind='check'")
	r := &uncertainRunner{}
	stats, err := (&Scheduler{Store: st, Runner: r}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 || !stats.Stopped || stats.Blocked != 1 {
		t.Fatal("uncertain executor outcome replayed")
	}
}
