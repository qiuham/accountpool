package main

import (
	"database/sql"
	"flag"
	"fmt"
	"github.com/ham/accountpool/backend/internal/claude"
	"os"
	"text/tabwriter"
	"time"

	"github.com/ham/accountpool/backend/internal/store"
)

// cmdProxy 管出口池。
//
// **并发单位是出口，不是任务**（DESIGN §4）。
// 同一个出口同一时刻只允许 1 个注册在跑——这是物理红线，
// 因为同 IP 并发注册是最强的关联特征，比指纹还强。
//
// 出口还必须与账号的地区自洽：日本出口 + UTC 时区，一次请求就露（DESIGN §3）。
func cmdProxy(st *store.Store, args []string) {
	if len(args) < 1 {
		die("用法: accountpool proxy add <url> [--kind residential] [--geo US] [--asn AS123]\n" +
			"      accountpool proxy list\n" +
			"      accountpool proxy test <id>        模拟占一次出口再释放\n" +
			"      accountpool proxy free <id>        手动释放（清冷却）")
	}
	switch args[0] {
	case "add":
		pos, rest := splitArgs(args[1:], map[string]bool{
			"kind": true, "geo": true, "asn": true})
		fs := flag.NewFlagSet("proxy add", flag.ExitOnError)
		kind := fs.String("kind", "residential", "datacenter/residential/mobile")
		geo := fs.String("geo", "US", "出口地区，必须与账号时区语言自洽")
		asn := fs.String("asn", "", "ASN，用于按段拉黑")
		fs.Parse(rest)
		if len(pos) < 1 {
			die("用法: accountpool proxy add <url> [--kind ...] [--geo ...]")
		}
		// 数据中心 IP 是社区公认的重灾区（LINUX DO 上有人用封号换来这个结论），
		// 所以登记时就把类型标清楚，调度器将来可以据此降权。
		id, err := st.AddProxy(store.Proxy{
			URL: pos[0], Kind: *kind, Geo: *geo, ASN: *asn})
		if err != nil {
			die("登记出口: %v", err)
		}
		fmt.Printf("已登记出口 id=%d  %s  [%s/%s]\n", id, pos[0], *kind, *geo)

	case "list", "ls":
		ps, err := st.ListProxies()
		if err != nil {
			die("列出口: %v", err)
		}
		if len(ps) == 0 {
			fmt.Println("（代理池为空，Claude 任务将等待代理配置，不会直连）")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "id\t状态\t类型\t地区\t失败\t冷却到\tURL")
		for _, p := range ps {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%s\t%s\n",
				p.ID, p.State, orDash(p.Kind), orDash(p.Geo), p.FailCount,
				orDash(p.CooldownUntil), short(p.URL, 44))
		}
		w.Flush()

	case "test":
		if len(args) < 2 {
			die("用法: accountpool proxy test <id>")
		}
		var targetID int64
		if _, err := fmt.Sscan(args[1], &targetID); err != nil || targetID <= 0 {
			die("无效出口 ID")
		}
		p, err := st.AcquireProxy("", sql.NullInt64{Int64: targetID, Valid: true})
		if err != nil {
			die("占用出口: %v", err)
		}
		if p == nil {
			fmt.Println("没有可用出口（全在冷却或在用）——正是并发锁生效的表现")
			return
		}
		client, err := claude.NewClient(p.URL)
		if err != nil {
			sErr := st.ReleaseProxy(p.ID, false, time.Minute)
			_ = sErr
			die("代理配置无效")
		}
		response, probeErr := client.HTTP.Get("https://example.com")
		success := probeErr == nil && response.StatusCode == 200
		if response != nil {
			response.Body.Close()
		}
		client.HTTP.CloseIdleConnections()
		if err := st.ReleaseProxy(p.ID, success, time.Minute); err != nil {
			die("记录代理检测结果失败")
		}
		if !success {
			die("代理 HTTPS 连通性检测失败，未访问 Claude")
		}
		fmt.Printf("代理 #%d 检测通过：HTTPS 200；未访问 Claude\n", p.ID)

	case "free":
		if len(args) < 2 {
			die("用法: accountpool proxy free <id>")
		}
		var id int64
		fmt.Sscanf(args[1], "%d", &id)
		if err := st.ReleaseProxy(id, true, 0); err != nil {
			die("释放: %v", err)
		}
		fmt.Printf("已释放出口 id=%d（清冷却）\n", id)

	default:
		die("未知 proxy 子命令: %s", args[0])
	}
}
