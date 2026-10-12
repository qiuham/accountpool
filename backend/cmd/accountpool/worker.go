package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"github.com/ham/accountpool/backend/internal/presentation"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/pool"
	"github.com/ham/accountpool/backend/internal/runner"
	"github.com/ham/accountpool/backend/internal/store"
)

// ── 适配器：把具体实现接到 pool 包的接口上 ──
//
// 为什么要接口：pool 是纯领域逻辑（状态机、限速、熔断），**不该知道**
// 它是通过子进程还是 HTTP 去干活。这样测试时能塞假实现，不用真注册账号。

type runnerAdapter struct{ r *runner.Runner }

func (a runnerAdapter) Run(ctx context.Context, spec pool.RunnerSpec) (*pool.RunnerResult, error) {
	rs := runner.Spec{
		Platform: spec.Platform,
		Account:  spec.Account,
		Steps:    spec.Steps,
		Options:  spec.Options,
		Proxy:    spec.Proxy,
		MailJWT:  spec.MailJWT,
	}
	if spec.Email != "" {
		e := spec.Email
		rs.Email = &e
	}
	res, _, err := a.r.Run(ctx, rs)
	if err != nil {
		return nil, err
	}
	creds := map[string]any{}
	if c := res.CredsOf(); c.Extra != nil {
		creds = c.Extra
	}
	if c := res.CredsOf(); c.SessionKey != "" {
		creds["sessionKey"] = c.SessionKey
	}
	return &pool.RunnerResult{
		Status:     res.Status,
		FailedStep: res.FailedStep,
		Reason:     res.Reason,
		Detail:     res.Detail,
		Artifacts:  res.Artifacts,
		Creds:      creds,
	}, nil
}

type mailAdapter struct{ mc *claude.MailClient }

func (a mailAdapter) EnsureMailbox(st *store.Store, acc *store.Account) (string, error) {
	return ensureMailbox(st, a.mc, acc)
}

// cmdWorker 是调度入口。把队列里的待跑任务一个个跑掉。
//
// 它自己**不决定**要不要等、要不要重试——那些是 pool.Scheduler 的事。
// 这里只负责：装好依赖、传对参数、把统计打出来。
func cmdWorker(st *store.Store, root string, args []string) {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	resume := fs.Bool("resume", false, "显式解除持久化调度暂停；不会重发中断任务")
	fs.Parse(args)
	if fs.NArg() != 0 {
		die("unexpected arguments")
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	r := runner.New(root)

	sch := &pool.Scheduler{
		Store:           st,
		RecoverExecutor: func() error { return runner.RecoverExecution(root) },
		Resume:          *resume,
		Runner:          runnerAdapter{r: r},
		Mail:            mailAdapter{mc: claude.NewMailClient()},
	}

	sch.LoadPolicy = runtimePolicy(st, sch, r)
	if err := sch.LoadPolicy(); err != nil {
		die("%v", err)
	}
	pending, _ := st.PendingJobs()
	fmt.Printf("开始调度：待跑 %d 个任务\n", pending)
	fmt.Printf("  策略：仅瞬态错误保留出口重试｜挑战持久暂停｜连续 %d 次失败熔断\n",
		sch.ConsecutiveFailMax)
	fmt.Println(strings.Repeat("-", 60))

	stats, err := sch.Run(ctx)
	if err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "调度中断: %v\n", err)
	}

	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("结果: 领 %d ｜ 成功 %d ｜ 失败 %d ｜ 受阻 %d ｜ 重试 %d\n",
		stats.Claimed, stats.OK, stats.Failed, stats.Blocked, stats.Retried)
	if stats.Stopped {
		fmt.Printf("\n⛔ 已熔断: %s\n", presentation.Reason(stats.StopWhy))
	}
	if left, _ := st.PendingJobs(); left > 0 {
		fmt.Printf("还有 %d 个待跑（可能是重试入队的）\n", left)
	}
	if err != nil {
		os.Exit(1)
	}
	if stats.Stopped {
		os.Exit(2)
	}
}

// cmdJob 是任务队列的手动操作。
func cmdJob(st *store.Store, args []string) {
	if len(args) < 1 {
		die("用法: accountpool job add <name> <kind> | accountpool job list")
	}
	switch args[0] {
	case "add":
		if len(args) < 3 {
			die("用法: accountpool job add <name> <kind>")
		}
		switch args[2] {
		case "register", "onboard", "warmup", "check", "oauth":
		default:
			die("未知任务步骤: %s", args[2])
		}
		a, err := st.GetAccount(args[1])
		if err != nil {
			die("找不到 %s: %v", args[1], err)
		}
		id, err := st.CreateJob(a.ID, args[2], sql.NullInt64{})
		if err != nil {
			die("建任务: %v", err)
		}
		fmt.Printf("已入队 job=%d: %s %s\n", id, a.Name, args[2])
	case "list", "ls":
		rows, err := st.ListJobs(50)
		if err != nil {
			die("列任务: %v", err)
		}
		if len(rows) == 0 {
			fmt.Println("（队列为空）")
			return
		}
		for _, j := range rows {
			fmt.Printf("  任务=%-4d 账号=%-4d %-9s %-9s 尝试=%d/%d %s\n",
				j.ID, j.AccountID, presentation.Step(j.Kind), presentation.State(j.State), j.Attempts, j.MaxAttempt,
				presentation.Reason(j.Reason))
		}
	default:
		die("未知 job 子命令: %s", args[0])
	}
}
