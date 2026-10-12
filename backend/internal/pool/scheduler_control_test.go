package pool

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ham/accountpool/backend/internal/store"
	"path/filepath"
	"testing"
	"time"
)

type failingRunner struct{ calls int }

func (r *failingRunner) Run(ctx context.Context, spec RunnerSpec) (*RunnerResult, error) {
	r.calls++
	return nil, errors.New("fixture startup failure")
}

func TestEmptyProxyPoolStopsBeforeCallingRunner(t *testing.T) {
	t.Setenv("ACCOUNTPOOL_REQUIRE_PROXY", "true")
	db, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	id, err := st.CreateAccount(&store.Account{Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := st.CreateJob(id, "check", sql.NullInt64{}); err != nil {
			t.Fatal(err)
		}
	}
	r := &failingRunner{}
	s := &Scheduler{Store: st, Runner: r, ConsecutiveFailMax: 3}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stats, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Stopped || r.calls != 0 {
		t.Fatalf("stopped=%v calls=%d", stats.Stopped, r.calls)
	}
}

func TestFailureStopsWithoutProxyPool(t *testing.T) {
	s := &Scheduler{ConsecutiveFailMax: 3}
	for i := 0; i < 3; i++ {
		s.releaseProxy(nil, false)
	}
	if !s.stopped {
		t.Fatal("three failures without proxy pool did not stop scheduler")
	}
}

func TestSuccessResetsConsecutiveFailuresWithoutProxy(t *testing.T) {
	s := &Scheduler{ConsecutiveFailMax: 3}
	s.releaseProxy(nil, false)
	s.releaseProxy(nil, false)
	s.releaseProxy(nil, true)
	s.releaseProxy(nil, false)
	if s.stopped || s.consecutiveFail != 1 {
		t.Fatalf("unexpected failure state: %d stopped=%v", s.consecutiveFail, s.stopped)
	}
}

type captureRunner struct {
	spec  RunnerSpec
	calls int
}

func (r *captureRunner) Run(ctx context.Context, spec RunnerSpec) (*RunnerResult, error) {
	r.spec = spec
	r.calls++
	return &RunnerResult{Status: "ok"}, nil
}
func TestConfiguredProxyCooldownStopsWithoutDefaultNetwork(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	id, _ := st.CreateAccount(&store.Account{Name: "fixture"})
	pid, _ := st.AddProxy(store.Proxy{URL: "http://127.0.0.1:9999"})
	st.BindAccountProxy(id, pid)
	if err := st.ReleaseProxy(pid, false, time.Hour); err != nil {
		t.Fatal(err)
	}
	st.CreateJob(id, "check", sql.NullInt64{})
	r := &captureRunner{}
	s := &Scheduler{Store: st, Runner: r}
	stats, err := s.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Stopped || r.calls != 0 {
		t.Fatalf("stats=%+v calls=%d", stats, r.calls)
	}
}
func TestSelectedProxyReachesRunnerAndIsReleased(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	id, _ := st.CreateAccount(&store.Account{Name: "fixture"})
	pid, _ := st.AddProxy(store.Proxy{URL: "http://127.0.0.1:9999"})
	st.BindAccountProxy(id, pid)
	st.CreateJob(id, "check", sql.NullInt64{})
	r := &captureRunner{}
	s := &Scheduler{Store: st, Runner: r}
	_, err = s.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.spec.Proxy == nil || *r.spec.Proxy != "http://127.0.0.1:9999" {
		t.Fatal("selected proxy missing")
	}
	ps, _ := st.ListProxies()
	if ps[0].State != "available" {
		t.Fatal("proxy leaked")
	}
}
