package pool

import (
	"context"
	"database/sql"
	"github.com/ham/accountpool/backend/internal/store"
	"path/filepath"
	"testing"
	"time"
)

type resultRunner struct {
	result RunnerResult
	calls  int
	spec   RunnerSpec
}

func (r *resultRunner) Run(_ context.Context, spec RunnerSpec) (*RunnerResult, error) {
	r.calls++
	r.spec = spec
	return &r.result, nil
}
func policyFixture(t *testing.T) (*store.Store, int64, int64, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)
	a, err := st.CreateAccount(&store.Account{Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	p1, err := st.AddProxy(store.Proxy{URL: "http://127.0.0.1:9001"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := st.AddProxy(store.Proxy{URL: "http://127.0.0.1:9002"})
	if err != nil {
		t.Fatal(err)
	}
	return st, a, p1, p2
}
func TestPinnedProxyIsHonored(t *testing.T) {
	st, a, _, p2 := policyFixture(t)
	st.CreateJob(a, "check", sql.NullInt64{Int64: p2, Valid: true})
	r := &resultRunner{result: RunnerResult{Status: "ok"}}
	s := &Scheduler{Store: st, Runner: r}
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.spec.Proxy == nil || *r.spec.Proxy != "http://127.0.0.1:9002" {
		t.Fatal("job used wrong proxy")
	}
}
func TestSuccessfulRequestResetsProxyFailures(t *testing.T) {
	st, a, p1, _ := policyFixture(t)
	st.ReleaseProxy(p1, false, -time.Minute)
	st.CreateJob(a, "check", sql.NullInt64{Int64: p1, Valid: true})
	r := &resultRunner{result: RunnerResult{Status: "ok"}}
	s := &Scheduler{Store: st, Runner: r}
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	ps, _ := st.ListProxies()
	if ps[0].FailCount != 0 {
		t.Fatal("successful request retained historical failures")
	}
}
func TestAccountFailureDoesNotPenalizeProxy(t *testing.T) {
	st, a, p1, _ := policyFixture(t)
	st.CreateJob(a, "check", sql.NullInt64{Int64: p1, Valid: true})
	r := &resultRunner{result: RunnerResult{Status: "blocked", Reason: "blocked:session-expired"}}
	s := &Scheduler{Store: st, Runner: r}
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	ps, _ := st.ListProxies()
	if ps[0].FailCount != 0 || ps[0].CooldownUntil != "" {
		t.Fatal("account failure penalized proxy")
	}
}
func TestChallengeStopsBeforeNextAccount(t *testing.T) {
	st, a, p1, _ := policyFixture(t)
	for i := 0; i < 2; i++ {
		st.CreateJob(a, "check", sql.NullInt64{Int64: p1, Valid: true})
	}
	r := &resultRunner{result: RunnerResult{Status: "blocked", Reason: "blocked:cf-challenge-human-required"}}
	s := &Scheduler{Store: st, Runner: r}
	stats, err := s.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Stopped || r.calls != 1 {
		t.Fatalf("challenge continued: calls=%d stopped=%v", r.calls, stats.Stopped)
	}
}

func TestPinnedCoolingProxyDoesNotSwitchToAvailableProxy(t *testing.T) {
	st, a, p1, _ := policyFixture(t)
	st.ReleaseProxy(p1, false, time.Hour)
	st.CreateJob(a, "check", sql.NullInt64{Int64: p1, Valid: true})
	r := &resultRunner{result: RunnerResult{Status: "ok"}}
	s := &Scheduler{Store: st, Runner: r}
	stats, err := s.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Stopped || r.calls != 0 {
		t.Fatal("cooling pinned proxy was bypassed")
	}
	jobs, err := st.ListJobs(10)
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].Attempts != 0 {
		t.Fatal("cooldown consumed an execution attempt")
	}
	job, err := st.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || !job.ProxyID.Valid || job.ProxyID.Int64 != p1 {
		t.Fatal("proxy binding lost while waiting")
	}
}
func TestTransientRetryKeepsProxyAndUsesShortBackoff(t *testing.T) {
	st, a, p1, _ := policyFixture(t)
	st.CreateJob(a, "check", sql.NullInt64{Int64: p1, Valid: true})
	r := &resultRunner{result: RunnerResult{Status: "failed", Reason: "navigation-timeout"}}
	s := &Scheduler{Store: st, Runner: r}
	stats, err := s.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 || !stats.Stopped {
		t.Fatalf("retry ignored cooldown: calls=%d", r.calls)
	}
	ps, _ := st.ListProxies()
	until, err := time.ParseInLocation("2006-01-02 15:04:05", ps[0].CooldownUntil, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if wait := time.Until(until); wait < 55*time.Second || wait > 61*time.Second {
		t.Fatalf("wrong transient backoff: %s", wait)
	}
	if ps[0].FailCount != 1 || ps[1].FailCount != 0 {
		t.Fatal("wrong proxy failure counts")
	}
}
func TestUnusedProxyPreservesFailureHistory(t *testing.T) {
	st, a, p1, _ := policyFixture(t)
	st.ReleaseProxy(p1, false, -time.Minute)
	st.CreateJob(a, "register", sql.NullInt64{Int64: p1, Valid: true})
	s := &Scheduler{Store: st, Runner: &captureRunner{}}
	if _, err := s.Run(context.Background()); err == nil {
		t.Fatal("missing mail provider was accepted")
	}
	ps, _ := st.ListProxies()
	if ps[0].State != "available" || ps[0].FailCount != 1 {
		t.Fatal("unused lease changed proxy history or leaked")
	}
}

func TestNullProxySurvivesClaimAndListing(t *testing.T) {
	st, a, _, _ := policyFixture(t)
	st.CreateJob(a, "check", sql.NullInt64{})
	jobs, err := st.ListJobs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ProxyID.Valid {
		t.Fatal("NULL proxy lost during listing")
	}
	job, err := st.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.ProxyID.Valid {
		t.Fatal("NULL proxy became valid zero id")
	}
}

func TestAccountProxyPersistsAcrossJobs(t *testing.T) {
	st, a, _, p2 := policyFixture(t)
	if _, err := st.CreateJob(a, "check", sql.NullInt64{Int64: p2, Valid: true}); err != nil {
		t.Fatal(err)
	}
	r := &resultRunner{result: RunnerResult{Status: "ok"}}
	if _, err := (&Scheduler{Store: st, Runner: r}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateJob(a, "check", sql.NullInt64{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Scheduler{Store: st, Runner: r}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.calls != 2 || r.spec.Proxy == nil || *r.spec.Proxy != "http://127.0.0.1:9002" {
		t.Fatal("new job lost account exit")
	}
}
