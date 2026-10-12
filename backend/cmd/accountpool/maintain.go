package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/ham/accountpool/backend/internal/presentation"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/pool"
	"github.com/ham/accountpool/backend/internal/runner"
	"github.com/ham/accountpool/backend/internal/store"
)

func cmdMaintain(st *store.Store, root string, args []string) {
	fs := flag.NewFlagSet("maintain", flag.ExitOnError)
	interval := fs.Duration("interval", time.Minute, "空闲或暂停时的巡检间隔，至少一分钟")
	once := fs.Bool("once", false, "只处理一轮然后退出")
	fs.Parse(args)
	if fs.NArg() != 0 {
		die("unexpected arguments")
	}
	policy := pool.Maintenance{EmailPrefix: "free-", TargetFree: 0, CheckInterval: 24 * time.Hour, MaxNewPerDay: 3}
	if *interval < time.Minute {
		die("interval must be >= 1m")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() { defer close(heartbeatDone); st.KeepExecutorHeartbeat(ctx) }()
	defer func() { cancel(); <-heartbeatDone }()
	r := runner.New(root)
	for {
		sch := &pool.Scheduler{Store: st, Runner: runnerAdapter{r: r}, Mail: mailAdapter{mc: claude.NewMailClient()}, RecoverExecutor: func() error { return runner.RecoverExecution(root) }, Maintenance: &policy, BaseCooldown: 30 * time.Minute, ConsecutiveFailMax: 3, MinGap: time.Minute, ReportInventory: func(i pool.Inventory) { _ = json.NewEncoder(os.Stdout).Encode(i) }}
		sch.LoadPolicy = runtimePolicy(st, sch, r)
		stats, err := sch.Run(ctx)
		if errors.Is(err, context.Canceled) {
			return
		}
		if err != nil {
			die("maintenance: %v", err)
		}
		if stats.Stopped {
			fmt.Printf("维护已暂停： %s\n", presentation.Reason(stats.StopWhy))
		}
		if *once {
			return
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
