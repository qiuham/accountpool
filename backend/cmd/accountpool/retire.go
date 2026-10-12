package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/lifecycle"
	"github.com/ham/accountpool/backend/internal/runner"
	"github.com/ham/accountpool/backend/internal/store"
)

func cmdRetire(st *store.Store, root string, args []string) {
	pos, rest := splitArgs(args, map[string]bool{})
	fs := flag.NewFlagSet("retire", flag.ExitOnError)
	purge := fs.Bool("purge", false, "删除邮箱和 profile；保留停用记录")
	disabled := fs.Bool("downstream-disabled", false, "确认下游已停用此账号")
	fs.Parse(rest)
	if len(pos) != 1 {
		die("用法: accountpool retire ACCOUNT [--purge --downstream-disabled]")
	}
	if err := lifecycle.Retire(context.Background(), st, runner.New(root), claude.NewMailClient(), pos[0], *purge, *disabled); err != nil {
		die("退役失败: %v", err)
	}
	fmt.Println("退役操作完成；下游停用记录和 OAuth 凭据保留")
}
