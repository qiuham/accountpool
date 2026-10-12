package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ham/accountpool/backend/internal/api"
	"github.com/ham/accountpool/backend/internal/store"
)

// cmdServe 起对外接口服务。
//
// **我们只提供接口，不主动推送**（DESIGN §2）。sub2api 定期来拉，
// 靠它自己的 crs_account_id 匹配做 upsert——同步复杂度全被它吸收。
// 节奏由它决定，所以我们的导出必须能反映"刚刚封掉"这类变化，
// 否则它会继续用死号。
func cmdServe(st *store.Store, args []string) {
	_, rest := splitArgs(args, map[string]bool{"addr": true, "user": true, "pass": true})
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8787", "监听地址")
	user := fs.String("user", os.Getenv("ACCOUNTPOOL_API_USER"), "CRS 兼容端点的登录名")
	pass := fs.String("pass", os.Getenv("ACCOUNTPOOL_API_PASSWORD"), "登录密码")
	fs.Parse(rest)

	srv := api.NewServer(st, *user, *pass)
	ready, err := st.ConsoleReady()
	if err != nil {
		die("%v", err)
	}
	if !ready {
		key := os.Getenv("ACCOUNTPOOL_SETUP_KEY")
		if key == "" {
			raw := make([]byte, 24)
			if _, err := rand.Read(raw); err != nil {
				die("%v", err)
			}
			key = hex.EncodeToString(raw)
		}
		srv.SetupKey = key
		fmt.Printf("首次管理员初始化密钥（完成初始化后失效）: %s\n", key)
	}
	mux := http.NewServeMux()
	srv.Routes(mux)
	mux.Handle("/", api.FrontendHandler(filepath.Join(findRoot(), "web")))

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		fmt.Printf("接口已起: http://%s\n", *addr)
		fmt.Println("  POST /web/auth/login                    登录（CRS 兼容）")
		fmt.Println("  GET  /admin/sync/export-accounts        导出（CRS 兼容）")
		fmt.Println("  GET  /healthz")
		fmt.Printf("  登录名: %s\n", *user)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			die("监听失败: %v", err)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()
	c, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	_ = httpSrv.Shutdown(c)
	fmt.Println("\n已停止")
}
