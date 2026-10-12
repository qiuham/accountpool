package pool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ham/accountpool/backend/internal/store"
)

// Scheduler 串行运行任务，并在连续失败时暂停。
type Scheduler struct {
	StartTrace      func(int64) func()
	Store           *store.Store
	Runner          Runner
	Mail            MailProvider
	RecoverExecutor func() error
	Resume          bool
	Maintenance     *Maintenance
	ReportInventory func(Inventory)
	LoadPolicy      func() error
	pauseErr        error

	// 单出口冷却基数。指数退避时按 fail_count 翻倍。
	BaseCooldown time.Duration
	// 全局熔断：连续失败达到这个数就停
	ConsecutiveFailMax int
	// 两次任务之间的最小间隔，避免打太快
	MinGap time.Duration

	consecutiveFail int
	stopped         bool
	stopReason      string
}

// Runner 是调度器需要执行侧提供的能力（用接口是为了能换成假实现做测试）。
type Runner interface {
	Run(ctx context.Context, spec RunnerSpec) (*RunnerResult, error)
}

// MailProvider 是调度器需要的邮箱能力。
type MailProvider interface {
	EnsureMailbox(st *store.Store, acc *store.Account) (string, error)
}

// RunnerSpec / RunnerResult 是 runner 包类型的镜像，避免 pool 依赖 runner 的具体实现。
type RunnerSpec struct {
	Platform string
	Account  string
	Steps    []string
	Email    string
	Proxy    *string
	Options  map[string]any
	MailJWT  string
}

type RunnerResult struct {
	Status     string
	FailedStep string
	Reason     string
	Detail     string
	Artifacts  string
	Creds      map[string]any
}

// Stats 是一轮调度的统计。跑完打出来，不靠猜。
type Stats struct {
	Claimed int
	OK      int
	Failed  int
	Blocked int
	Retried int
	Stopped bool
	StopWhy string
}

// Run 一直跑到没有待跑任务（或触发熔断/上下文取消）。
func (s *Scheduler) Run(ctx context.Context) (Stats, error) {
	var st Stats
	unlock, err := s.Store.LockWorker()
	if err != nil {
		return st, err
	}
	defer unlock()
	if s.RecoverExecutor != nil {
		if err := s.RecoverExecutor(); err != nil {
			return st, err
		}
	}
	if _, err := s.Store.RecoverInterruptedJobs(); err != nil {
		return st, err
	}
	if s.Resume {
		if err := s.Store.ResumeScheduler(); err != nil {
			return st, err
		}
	}
	if reason, err := s.Store.SchedulerPause(); err != nil {
		return st, err
	} else if reason != "" {
		st.Stopped = true
		st.StopWhy = reason
		return st, nil
	}
	for {
		if s.stopped {
			st.Stopped, st.StopWhy = true, s.stopReason
			return st, s.pauseErr
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		default:
		}

		if reason, err := s.Store.SchedulerPause(); err != nil {
			return st, err
		} else if reason != "" {
			st.Stopped = true
			st.StopWhy = reason
			return st, nil
		}
		if s.LoadPolicy != nil {
			if err := s.LoadPolicy(); err != nil {
				return st, fmt.Errorf("load policy: %w", err)
			}
		}

		if s.Maintenance != nil {
			inventory, err := s.Maintenance.Reconcile(s.Store)
			if err != nil {
				return st, err
			}
			if s.ReportInventory != nil {
				s.ReportInventory(inventory)
			}
		}
		job, err := s.Store.ClaimJob()
		if err != nil {
			return st, fmt.Errorf("领任务: %w", err)
		}
		if job == nil {
			return st, nil // 没有待跑的了
		}
		st.Claimed++

		outcome, err := s.runOne(ctx, job)
		if err != nil {
			finishErr := s.Store.FinishJob(job.ID, "failed", "scheduler-error", err.Error(), "")
			pauseErr := s.Store.PauseScheduler("scheduler-error: inspect task outcome before resume", time.Time{})
			return st, errors.Join(err, finishErr, pauseErr)
		}
		switch outcome {
		case "ok":
			st.OK++
			s.consecutiveFail = 0
		case "blocked":
			st.Blocked++
		default:
			st.Failed++
			if outcome == "retried" {
				st.Retried++
			}
		}
		if s.MinGap > 0 && !s.stopped {
			timer := time.NewTimer(s.MinGap)
			select {
			case <-ctx.Done():
				timer.Stop()
				return st, ctx.Err()
			case <-timer.C:
			}
		}
	}
}

// runOne 跑一个任务。
//
// **关键区分**（决定了要不要重试）：
//
//	result.status = failed   → 值得原样重试（邮件没到、网络抖动）
//	result.status = blocked  → 重试无用（CF 过不去、被封、被验证拦住）
//
// 把这两者混在一起会导致"对着过不去的墙反复撞"，既浪费额度又制造异常记录。
func (s *Scheduler) runOne(ctx context.Context, job *store.Job) (string, error) {
	if s.StartTrace != nil {
		done := s.StartTrace(job.ID)
		defer done()
	}
	acc, err := s.Store.GetAccountByID(job.AccountID)
	if err != nil {
		return "", fmt.Errorf("取账号: %w", err)
	}

	if acc.State == string(MailboxMissing) || acc.State == string(MailboxConfigError) {
		return "blocked", s.Store.FinishJob(job.ID, "blocked", "mailbox-prerequisite-unresolved", "", "")
	}
	if s.Maintenance != nil && job.Kind == "warmup" && s.Maintenance.ChatInterval < time.Hour {
		return "blocked", s.Store.FinishJob(job.ID, "blocked", "scheduled-chat-disabled", "", "")
	}

	if s.Maintenance != nil && (acc.AccessToken != "" || acc.RefreshToken != "") {
		return "blocked", s.Store.FinishJob(job.ID, "blocked", "oauth-account-excluded", "", "")
	}
	if s.Maintenance != nil && job.Kind == "warmup" && (acc.Plan != "" && acc.Plan != "claude_free" || acc.SubscribedAt != "") {
		return "blocked", s.Store.FinishJob(job.ID, "blocked", "paid-account-excluded", "", "")
	}
	bound, err := s.Store.AccountProxy(acc.ID)
	if err != nil {
		return "", err
	}
	if bound != nil {
		if job.ProxyID.Valid && job.ProxyID.Int64 != bound.ID {
			return "", fmt.Errorf("job proxy conflicts with account binding")
		}
		job.ProxyID = nullInt(bound.ID)
	}
	if State(acc.State).Terminal() || acc.RetiredAt != "" {
		if err := s.Store.FinishJob(job.ID, "blocked", "account-retired", "", ""); err != nil {
			return "", err
		}
		return "blocked", nil
	}

	// Use the account proxy when bound; unbound accounts normally connect directly.
	var proxy *store.Proxy
	if job.ProxyID.Valid || os.Getenv("ACCOUNTPOOL_REQUIRE_PROXY") == "true" {
		proxy, err = s.Store.AcquireProxy("", job.ProxyID)
		if err != nil {
			return "", err
		}
		if proxy == nil {
			if err := s.Store.RequeueJob(job.ID, false); err != nil {
				return "", err
			}
			s.stopped, s.stopReason = true, "没有可用出口，调度暂停"
			return "blocked", nil
		}
		defer func() {
			if proxy != nil {
				s.Store.UnlockProxy(proxy.ID)
			}
		}()
		if err := s.Store.BindJobProxy(job.ID, proxy.ID); err != nil {
			return "", err
		}
		if err := s.Store.BindAccountProxy(acc.ID, proxy.ID); err != nil {
			return "", err
		}
		job.ProxyID = nullInt(proxy.ID)
	}
	spec := RunnerSpec{
		Platform: "claude",
		Account:  acc.Name,
		Steps:    []string{job.Kind},
		Options:  map[string]any{},
	}
	if job.Kind == "warmup" && s.Maintenance != nil {
		prompt, err := store.RandomPrompt(s.Maintenance.ChatPrompts)
		if err != nil {
			return "", err
		}
		spec.Options["prompt"] = prompt
	}
	if proxy != nil {
		spec.Proxy = &proxy.URL
	}
	if acc.Email != "" {
		spec.Email = acc.Email
	}

	// 注册需要收信 JWT。邮箱是物料，归号池管。
	if job.Kind == "register" {
		if s.Mail == nil {
			return "", fmt.Errorf("未配置邮箱提供者，无法注册")
		}
		jwt, err := s.Mail.EnsureMailbox(s.Store, acc)
		if err != nil {
			return "blocked", s.finish(job, acc, "blocked", "mailbox-create-failed", err.Error(), "")
		}
		if acc.State == string(MailboxCreateFailed) {
			if err := s.Store.ClearAccountRetry(acc.ID); err != nil {
				return "", err
			}
		}
		spec.MailJWT = jwt
		if acc.Email != "" {
			spec.Email = acc.Email
		}
	}

	if err := PrepareStep(s.Store, acc, job.Kind); err != nil {
		return "", err
	}
	res, err := s.Runner.Run(ctx, spec)
	if err != nil {
		// 执行侧压根没起来——这是基础设施故障，可以重试
		if proxy != nil {
			s.Store.UnlockProxy(proxy.ID)
			proxy = nil
		}
		var unknown interface{ OutcomeUnknown() bool }
		if errors.As(err, &unknown) && unknown.OutcomeUnknown() {
			s.pause("interrupted-unknown-outcome", time.Time{})
			return "blocked", s.finish(job, acc, "blocked", "interrupted-unknown-outcome", err.Error(), "")
		}
		s.releaseProxy(nil, false)
		return s.retryOrFail(job, acc, "runner-error", err.Error())
	}

	if err := ApplyOutcome(s.Store, acc, job.Kind, res.Status, res.Reason, res.Creds); err != nil {
		return "", err
	}
	if res.Status == "ok" {
		s.consecutiveFail = 0

		if err := s.finish(job, acc, "ok", "", "", res.Artifacts); err != nil {
			return "", err
		}
		s.releaseProxy(proxy, true)
		proxy = nil
		return "ok", nil
	}

	// Account/local failures do not establish a proxy failure. Challenges pause the
	// entire run; only explicitly transient failures are eligible for retry.
	challenge := res.Reason == "blocked:access-denied" || strings.HasPrefix(res.Reason, "blocked:cf-challenge") || res.Reason == "blocked:rate-limited"
	transient := res.Reason == "navigation-timeout" || res.Reason == "navigation-error" || res.Reason == "page-not-ready" || res.Reason == "page-http-error"
	if challenge {
		s.releaseProxy(proxy, false)
		proxy = nil
	} else if transient {
		if proxy != nil {
			if err := s.Store.ReleaseProxy(proxy.ID, false, exponentialCooldown(time.Minute, proxy.FailCount)); err != nil {
				return "", err
			}
			proxy = nil
		}
		s.releaseProxy(nil, false)
	} else {
		s.releaseProxy(nil, false)
	}
	if reason, until, ok := AccessPause(res.Reason, s.BaseCooldown); ok {
		if s.Maintenance != nil && until.IsZero() {
			delay := s.BaseCooldown
			if delay < time.Minute {
				delay = 30 * time.Minute
			}
			until = time.Now().Add(delay)
		}
		s.pause(reason, until)
	}

	if res.Status == "blocked" {
		return "blocked", s.finish(job, acc, "blocked", res.Reason, res.Detail, res.Artifacts)
	}
	if transient && s.Maintenance == nil {
		outcome, err := s.retryOrFail(job, acc, res.Reason, res.Detail)
		if err == nil && outcome == "retried" {
			s.pause("transient-backoff: "+res.Reason, time.Now().Add(exponentialCooldown(time.Minute, job.Attempts-1)))
		}
		return outcome, err
	}
	return "failed", s.finish(job, acc, "failed", res.Reason, res.Detail, res.Artifacts)
}

// retryOrFail 决定"再试一次还是认输"。
//
// 重试保留原出口绑定，等待其冷却。
func (s *Scheduler) retryOrFail(job *store.Job, acc *store.Account, reason, detail string) (string, error) {
	if s.Maintenance != nil {
		return "failed", s.finish(job, acc, "failed", reason, detail, "")
	}
	if job.Attempts < job.MaxAttempt {
		// 重新入队，保留原出口。
		if err := s.Store.RequeueJob(job.ID, true); err != nil {
			return "", err
		}
		s.Store.AddEvent(acc.ID, job.Kind, acc.State, acc.State,
			fmt.Sprintf(`{"retry":%d,"reason":"%s"}`, job.Attempts, reason))
		return "retried", nil
	}
	return "failed", s.finish(job, acc, "failed", reason, detail, "")
}

func (s *Scheduler) finish(job *store.Job, acc *store.Account, state, reason, detail, out string) error {
	if reason == "mailbox-create-failed" && mailboxBlockState(detail) != "" {
		reason = "mailbox-prerequisite-failed"
	}
	if err := s.Store.FinishJob(job.ID, state, reason, detail, out); err != nil {
		return err
	}
	if reason == "mailbox-create-failed" {
		if err := s.Store.ScheduleMailboxRetry(acc.ID, reason); err != nil {
			return err
		}
		acc.State = string(MailboxCreateFailed)
		return nil
	}
	if (state == "failed" || state == "blocked") && (mailboxBlockState(reason) != "" || reason == "mailbox-prerequisite-failed") {
		target := string(mailboxBlockState(reason))
		if reason == "mailbox-prerequisite-failed" {
			target = string(mailboxBlockState(detail))
		}
		if err := s.Store.ClearAccountRetry(acc.ID); err != nil {
			return err
		}
		if err := s.Store.UpdateState(acc.ID, target, "mailbox-blocked", reason); err != nil {
			return err
		}
		acc.State = target
	}
	if job.Kind == "onboard" && state == "ok" {
		if err := s.Store.ClearAccountRetry(acc.ID); err != nil {
			return err
		}
		_, err := s.Store.CreateJob(acc.ID, "check", sql.NullInt64{})
		return err
	}
	if reason == "onboarding-required" {
		if err := s.Store.ClearAccountRetry(acc.ID); err != nil {
			return err
		}
		_, err := s.Store.CreateJob(acc.ID, "onboard", sql.NullInt64{})
		return err
	}
	if s.Maintenance != nil {
		if state == "ok" {
			if err := s.Store.ClearAccountRetry(acc.ID); err != nil {
				return err
			}
		}
		if (state == "failed" || state == "blocked") && RetryableFailure(job.Kind, reason, acc) {
			step := job.Kind
			if reason == "blocked:session-expired" || reason == "blocked:no-session-key" || reason == "blocked:no-bundle" {
				step = "register"
			}
			if err := s.Store.ScheduleAccountRetry(acc.ID, step, reason, s.BaseCooldown); err != nil {
				return err
			}
			acc.State = string(Cooldown)
		}
	}
	if reason != "" {
		return s.Store.UpdateFields(acc.ID, map[string]any{"last_error": reason})
	}
	return nil
}

func (s *Scheduler) releaseProxy(p *store.Proxy, ok bool) {
	cd := s.BaseCooldown
	if cd == 0 {
		cd = 30 * time.Minute
	}
	if !ok && p != nil {
		cd = exponentialCooldown(cd, p.FailCount)
	}
	if p != nil {
		s.Store.ReleaseProxy(p.ID, ok, cd)
	}
	// 连续失败达到阈值 → 全局熔断
	if !ok {
		s.consecutiveFail++
		if s.ConsecutiveFailMax > 0 && s.consecutiveFail >= s.ConsecutiveFailMax {
			s.pause(fmt.Sprintf("连续 %d 次失败，调度暂停，等待排查", s.consecutiveFail), time.Now().Add(30*time.Minute))
		}
	} else {
		s.consecutiveFail = 0
	}
}

func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// nullInt 便于构造 sql.NullInt64。
func nullInt(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: v > 0}
}

func exponentialCooldown(base time.Duration, failures int) time.Duration {
	for i := 0; i < failures && i < 6; i++ {
		base *= 2
	}
	return base
}

func (s *Scheduler) pause(reason string, until time.Time) {
	s.stopped, s.stopReason = true, reason
	if s.Store != nil {
		s.pauseErr = s.Store.PauseScheduler(reason, until)
	}
}
