package pool

import (
	"context"
	"database/sql"
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
	"time"
)

type pauseRunner struct {
	store *store.Store
	calls int
}

func (r *pauseRunner) Run(ctx context.Context, s RunnerSpec) (*RunnerResult, error) {
	r.calls++
	if err := r.store.PauseScheduler("operator-paused", time.Time{}); err != nil {
		return nil, err
	}
	return &RunnerResult{Status: "ok"}, nil
}
func TestOperatorPauseStopsNextClaim(t *testing.T) {
	st, a, p, _ := policyFixture(t)
	if _, err := st.CreateJob(a, "check", sql.NullInt64{Int64: p, Valid: true}); err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateAccount(&store.Account{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateJob(other, "check", sql.NullInt64{Int64: p, Valid: true}); err != nil {
		t.Fatal(err)
	}
	r := &pauseRunner{store: st}
	s := &Scheduler{Store: st, Runner: r}
	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 || !result.Stopped || result.StopWhy != "operator-paused" {
		t.Fatal(r.calls, result)
	}
}
