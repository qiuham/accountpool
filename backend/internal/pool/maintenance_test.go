package pool

import (
	"context"
	"database/sql"
	"github.com/ham/accountpool/backend/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func maintenanceStore(t *testing.T) *store.Store {
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
	return st
}
func policy() Maintenance {
	return Maintenance{TargetFree: 2, CheckInterval: 24 * time.Hour, MaxNewPerDay: 3}
}
func freeFixture(now time.Time) store.MaintenanceAccount {
	return store.MaintenanceAccount{Account: store.Account{ID: 1, Name: "existing", State: "registered", Plan: "claude_free", SessionKey: "fixture"}, Jobs: map[string]store.Job{"onboard": {ID: 1, State: "ok"}, "check": {ID: 2, State: "ok", FinishedAt: now.Format("2006-01-02 15:04:05")}}}
}
func TestMaintenanceInventoryAndExclusions(t *testing.T) {
	now := time.Now()
	free := freeFixture(now)
	for _, tc := range []struct {
		name                              string
		edit                              func(*store.MaintenanceAccount)
		free, reserved, paid, unavailable int
		step                              string
	}{
		{"ready", func(a *store.MaintenanceAccount) {}, 1, 0, 0, 0, "create"},
		{"paid", func(a *store.MaintenanceAccount) { a.Plan = "claude_pro" }, 0, 0, 1, 0, "oauth"},
		{"oauth", func(a *store.MaintenanceAccount) { a.AccessToken = "fixture" }, 0, 0, 1, 0, "create"},
		{"subscribed", func(a *store.MaintenanceAccount) { a.SubscribedAt = "2026-10-01" }, 0, 0, 1, 0, "create"},
		{"mailbox-missing", func(a *store.MaintenanceAccount) { a.State = "mailbox_missing" }, 0, 1, 0, 0, "create"},
		{"blocked", func(a *store.MaintenanceAccount) { a.State = "manual" }, 0, 0, 0, 1, "create"},
		{"retired", func(a *store.MaintenanceAccount) { a.RetiredAt = "2026-10-01" }, 0, 0, 0, 1, "create"},
		{"unknown", func(a *store.MaintenanceAccount) { a.Plan = "" }, 0, 1, 0, 0, "create"},
		{"stale", func(a *store.MaintenanceAccount) {
			a.Jobs["check"] = store.Job{ID: 2, State: "ok", FinishedAt: now.Add(-25 * time.Hour).Format("2006-01-02 15:04:05")}
		}, 0, 1, 0, 0, "check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := freeFixture(now)
			tc.edit(&a)
			i, _, err := policy().Decide([]store.MaintenanceAccount{a}, now)
			if err != nil || i.Free != tc.free || i.Reserved != tc.reserved || i.Paid != tc.paid || i.Unavailable != tc.unavailable || i.Action != tc.step {
				t.Fatalf("inventory=%+v err=%v", i, err)
			}
		})
	}
	p := policy()
	p.TargetFree = 1
	i, a, err := p.Decide([]store.MaintenanceAccount{free}, now)
	if err != nil || a != nil || i.Free != 1 {
		t.Fatalf("overfilled: %+v %v", i, err)
	}
}
func TestMaintenanceReservationsAndDailyLimit(t *testing.T) {
	st := maintenanceStore(t)
	p := policy()
	for n := 0; n < 5; n++ {
		if _, err := p.Reconcile(st); err != nil {
			t.Fatal(err)
		}
	}
	accounts, err := st.ListAccounts()
	if err != nil || len(accounts) != 2 {
		t.Fatalf("accounts=%d err=%v", len(accounts), err)
	}
	jobs, err := st.ListJobs(20)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs=%d err=%v", len(jobs), err)
	}
	p.TargetFree = 10
	p.MaxNewPerDay = 2
	i, err := p.Reconcile(st)
	if err != nil || i.CreatedToday != 2 || i.Action != "" {
		t.Fatalf("daily cap: %+v %v", i, err)
	}
}

type maintenanceRunner struct {
	calls []string
	paid  bool
	fail  bool
}

func (r *maintenanceRunner) Run(_ context.Context, s RunnerSpec) (*RunnerResult, error) {
	step := s.Steps[0]
	r.calls = append(r.calls, step)
	if r.fail {
		return &RunnerResult{Status: "blocked", Reason: "blocked:cf-challenge-human-required"}, nil
	}
	values := map[string]any{"sessionKey": "fixture"}
	if step == "check" {
		values["plan"] = "claude_free"
		if r.paid {
			values["plan"] = "claude_pro"
			values["subscribed"] = true
		}
	}
	if step == "oauth" {
		values["accessToken"] = "fixture-access"
		values["refreshToken"] = "fixture-refresh"
		values["expiresAt"] = time.Now().Add(time.Hour).UnixMilli()
		values["scopes"] = []string{"user:inference"}
	}
	return &RunnerResult{Status: "ok", Creds: values}, nil
}

type maintenanceMail struct{}

func (maintenanceMail) EnsureMailbox(_ *store.Store, a *store.Account) (string, error) {
	a.Email = "fixture@example.test"
	return "fixture", nil
}
func TestMaintenanceFullPipelineAndPaidReplacement(t *testing.T) {
	st := maintenanceStore(t)
	p := policy()
	p.TargetFree = 1
	r := &maintenanceRunner{}
	sch := &Scheduler{Store: st, Runner: r, Mail: maintenanceMail{}, Maintenance: &p}
	stats, err := sch.Run(context.Background())
	if err != nil || stats.OK != 3 {
		t.Fatalf("stats=%+v err=%v calls=%v", stats, err, r.calls)
	}
	if got := r.calls; len(got) != 3 || got[0] != "register" || got[1] != "onboard" || got[2] != "check" {
		t.Fatal(got)
	}
	accounts, _ := st.ListAccounts()
	acc, _ := st.GetAccount(accounts[0].Name)
	// A subscription is discovered by the next health check.
	_, err = st.CreateJob(acc.ID, "check", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	r.paid = true
	p.MaxNewPerDay = 1
	sch = &Scheduler{Store: st, Runner: r, Mail: maintenanceMail{}, Maintenance: &p}
	stats, err = sch.Run(context.Background())
	if err != nil || stats.OK != 2 {
		t.Fatalf("paid stats=%+v err=%v", stats, err)
	}
	acc, _ = st.GetAccount(acc.Name)
	if acc.State != "subscribed" || acc.AccessToken == "" || acc.SubscribedAt == "" {
		t.Fatal("paid handoff not persisted")
	}
	p.MaxNewPerDay = 2
	i, err := p.Reconcile(st)
	if err != nil || i.Paid != 1 || i.Free != 0 || i.Action != "create" {
		t.Fatalf("replacement: %+v %v", i, err)
	}
	// Restart cannot reauthorize a paid account or enqueue duplicate registrations.
	i, err = p.Reconcile(st)
	if err != nil || i.Action != "" || i.Reserved != 1 {
		t.Fatalf("restart: %+v %v", i, err)
	}
}
func TestMaintenanceChallengePauseSurvivesRestart(t *testing.T) {
	st := maintenanceStore(t)
	p := policy()
	r := &maintenanceRunner{fail: true}
	for n := 0; n < 2; n++ {
		sch := &Scheduler{Store: st, Runner: r, Mail: maintenanceMail{}, Maintenance: &p}
		stats, err := sch.Run(context.Background())
		if err != nil || !stats.Stopped {
			t.Fatalf("%+v %v", stats, err)
		}
	}
	if len(r.calls) != 1 {
		t.Fatal("challenge replayed")
	}
	accounts, _ := st.ListAccounts()
	if len(accounts) != 1 {
		t.Fatal("created during pause")
	}
}
func TestMaintenanceTransactionRollsBackOnQueueFailure(t *testing.T) {
	st := maintenanceStore(t)
	if _, err := st.DB().Exec(`CREATE TRIGGER reject_job BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := policy().Reconcile(st); err == nil {
		t.Fatal("expected write failure")
	}
	accounts, _ := st.ListAccounts()
	if len(accounts) != 0 {
		t.Fatal("orphan reservation committed")
	}
}
func TestMaintenanceDoesNotReplayFailedOAuth(t *testing.T) {
	a := freeFixture(time.Now())
	a.Plan = "claude_pro"
	a.Jobs["oauth"] = store.Job{ID: 3, State: "blocked"}
	p := policy()
	p.TargetFree = 0
	_, action, err := p.Decide([]store.MaintenanceAccount{a}, time.Now())
	if err != nil || action != nil {
		t.Fatalf("replayed: %+v %v", action, err)
	}
}
func TestMaintenanceReloginAfterExpiredSession(t *testing.T) {
	a := freeFixture(time.Now())
	a.State = "session_expired"
	a.LastHealthyCheckID = 2
	a.Jobs["register"] = store.Job{ID: 1, State: "ok"}
	a.Jobs["check"] = store.Job{ID: 4, State: "blocked", Reason: "blocked:session-expired"}
	_, action, err := policy().Decide([]store.MaintenanceAccount{a}, time.Now())
	if err != nil || action == nil || action.Kind != "register" {
		t.Fatalf("relogin: %+v %v", action, err)
	}
	a.Jobs["register"] = store.Job{ID: 5, State: "failed"}
	if step := nextMaintenanceStep(a, time.Now(), time.Hour); step != "" {
		t.Fatal("failed login replayed")
	}
}

func TestMaintenanceZeroTargetAndPendingReservation(t *testing.T) {
	p := policy()
	p.TargetFree = 0
	if _, a, err := p.Decide(nil, time.Now()); err != nil || a != nil {
		t.Fatalf("zero target created: %v", err)
	}
	p.TargetFree = 1
	pending := store.MaintenanceAccount{Account: store.Account{State: "pending"}, Busy: true, Jobs: map[string]store.Job{}}
	i, a, err := p.Decide([]store.MaintenanceAccount{pending}, time.Now())
	if err != nil || a != nil || i.Reserved != 1 {
		t.Fatalf("pending not reserved: %+v %v", i, err)
	}
}
func TestMaintenanceWorkerLeaseBlocksSecondController(t *testing.T) {
	st := maintenanceStore(t)
	unlock, err := st.LockWorker()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	p := policy()
	sch := &Scheduler{Store: st, Maintenance: &p}
	if _, err := sch.Run(context.Background()); err == nil {
		t.Fatal("second controller entered")
	}
	accounts, _ := st.ListAccounts()
	if len(accounts) != 0 {
		t.Fatal("created without lease")
	}
}

func TestMaintenanceFinishWriteFailureStops(t *testing.T) {
	st := maintenanceStore(t)
	if _, err := st.DB().Exec(`CREATE TRIGGER reject_finish BEFORE UPDATE OF finished_at ON jobs BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	p := policy()
	r := &maintenanceRunner{}
	sch := &Scheduler{Store: st, Maintenance: &p, Runner: r, Mail: maintenanceMail{}}
	if _, err := sch.Run(context.Background()); err == nil {
		t.Fatal("lost finish accepted")
	}
	if len(r.calls) != 1 {
		t.Fatal("continued after database failure")
	}
	reason, err := st.SchedulerPause()
	if err != nil || reason == "" {
		t.Fatal("missing persistent pause")
	}
}

type expiredMaintenanceRunner struct{ calls []string }

func (r *expiredMaintenanceRunner) Run(_ context.Context, s RunnerSpec) (*RunnerResult, error) {
	step := s.Steps[0]
	r.calls = append(r.calls, step)
	if step == "check" {
		return &RunnerResult{Status: "blocked", Reason: "blocked:session-expired"}, nil
	}
	return &RunnerResult{Status: "ok", Creds: map[string]any{"sessionKey": "fixture"}}, nil
}
func TestMaintenanceReloginWaitsForCooldown(t *testing.T) {
	st := maintenanceStore(t)
	id, err := st.CreateAccount(&store.Account{Name: "existing", State: "registered"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"register", "onboard", "check"} {
		j, e := st.CreateJob(id, step, sql.NullInt64{})
		if e != nil {
			t.Fatal(e)
		}
		if e = st.FinishJob(j, "ok", "", "", ""); e != nil {
			t.Fatal(e)
		}
	}
	st.CreateJob(id, "check", sql.NullInt64{})
	p := policy()
	p.TargetFree = 1
	r := &expiredMaintenanceRunner{}
	sch := &Scheduler{Store: st, Runner: r, Mail: maintenanceMail{}, Maintenance: &p}
	stats, err := sch.Run(context.Background())
	if err != nil || stats.Claimed != 1 {
		t.Fatalf("%+v %v", stats, err)
	}
	a, _ := st.GetAccountByID(id)
	if a.State != "cooldown" {
		t.Fatal(a.State)
	}
	if _, err := sch.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Fatal("retried before cooldown")
	}
	if _, err := st.DB().Exec("UPDATE account_retries SET due_at=? WHERE account_id=?", time.Now().Add(-time.Second).Unix(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := sch.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 3 || r.calls[1] != "register" || r.calls[2] != "check" {
		t.Fatal(r.calls)
	}
	a, _ = st.GetAccountByID(id)
	if a.State != "cooldown" {
		t.Fatal("failed recovery should cool down")
	}

}

func TestMaintenanceNeverReloginsOAuthAccounts(t *testing.T) {
	for _, state := range []string{"pending", "registered", "session_expired", "subscribed"} {
		a := freeFixture(time.Now())
		a.State = state
		a.AccessToken = "fixture"
		a.Jobs["check"] = store.Job{ID: 9, State: "blocked", Reason: "blocked:session-expired"}
		if step := nextMaintenanceStep(a, time.Now(), time.Hour); step != "" {
			t.Fatalf("OAuth state %s scheduled %s", state, step)
		}
	}
}

type bannedMaintenanceRunner struct{ maintenanceRunner }

func (r *bannedMaintenanceRunner) Run(ctx context.Context, s RunnerSpec) (*RunnerResult, error) {
	if s.Account == "old" {
		return &RunnerResult{Status: "blocked", Reason: "blocked:banned", Creds: map[string]any{"health": "banned"}}, nil
	}
	return r.maintenanceRunner.Run(ctx, s)
}
func TestMaintenanceReplacesConfirmedDeadAccount(t *testing.T) {
	st := maintenanceStore(t)
	id, err := st.CreateAccount(&store.Account{Name: "old", State: "registered"})
	if err != nil {
		t.Fatal(err)
	}
	st.CreateJob(id, "check", sql.NullInt64{})
	p := policy()
	p.TargetFree = 1
	r := &bannedMaintenanceRunner{}
	sch := &Scheduler{Store: st, Maintenance: &p, Runner: r, Mail: maintenanceMail{}}
	stats, err := sch.Run(context.Background())
	if err != nil || stats.Stopped || stats.Blocked != 1 || stats.OK != 3 {
		t.Fatalf("replacement failed: %+v %v", stats, err)
	}
	a, _ := st.GetAccountByID(id)
	if a.State != "burned" {
		t.Fatal("dead account not excluded")
	}
	i, err := p.Reconcile(st)
	if err != nil || i.Free != 1 || i.Unavailable != 1 || i.Action != "" {
		t.Fatalf("wrong waterline %+v %v", i, err)
	}
}

func TestMaintenanceConfigurablePrefix(t *testing.T) {
	for _, prefix := range []string{"pool-", "custom_", ""} {
		p := policy()
		p.EmailPrefix = prefix
		_, action, err := p.Decide(nil, time.Now())
		if err != nil || action == nil || action.NewAccount.Name != prefix {
			t.Fatalf("prefix %q: action=%+v err=%v", prefix, action, err)
		}
	}
	for _, prefix := range []string{"bad@", "a.b", "../", "空", "space ", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		p := policy()
		p.EmailPrefix = prefix
		if p.Validate() == nil {
			t.Fatalf("accepted invalid prefix %q", prefix)
		}
	}
}
func TestMaintenancePrefixChangePreservesDailyCap(t *testing.T) {
	st := maintenanceStore(t)
	p := policy()
	p.TargetFree = 10
	p.MaxNewPerDay = 1
	p.EmailPrefix = "first-"
	if _, err := p.Reconcile(st); err != nil {
		t.Fatal(err)
	}
	p.EmailPrefix = "second-"
	i, err := p.Reconcile(st)
	if err != nil || i.CreatedToday != 1 || i.Action != "" {
		t.Fatalf("cap reset: %+v %v", i, err)
	}
}
