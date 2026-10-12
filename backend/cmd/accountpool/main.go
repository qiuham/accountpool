// accountpool 是号池服务端：供号、养号、查状态、被下游拉走。
//
// 职责（DESIGN §0）
//
//	持有：账号生命周期、调度、冷却、出口分配、墓碑、成本
//	不持有：账号的运行时状态（那以 sub2api 为准）
//
// 执行侧（Python）只做浏览器相关的事：register / onboard / warmup。
// 封禁/订阅检测、OAuth、邮箱和退役由 Go 执行。
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"github.com/ham/accountpool/backend/internal/presentation"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/pool"
	"github.com/ham/accountpool/backend/internal/runner"
	"github.com/ham/accountpool/backend/internal/store"
)

const usage = `accountpool · 账号池管理
账号供给与生命周期管理

用法:
  accountpool add <name> [--email X] [--proxy URL]     建号（只入库，不注册）
  accountpool list [--state S]                          列号
  accountpool show <name>                               看单个号 + 事件时间线
  accountpool run <name> --steps register,onboard       跑任务（调执行侧）
  accountpool serve [--addr 127.0.0.1:8787]             起对外接口（CRS 兼容，供 sub2api 拉）
  accountpool maintain                常驻维护免费号池（订阅账号自动转 OAuth）
  accountpool worker [--resume]          调度：跑光队列里的待跑任务
  accountpool job add <name> <kind>                     手动入队
  accountpool job list                                  看队列
  accountpool proxy add <url> | list | test <id>       出口池（并发单位是出口）
  accountpool mail list | new <name> | wait <jwt>       邮箱池（已搬到 Go）
  accountpool retire <name> [--purge --downstream-disabled]  退役/清理账号
  accountpool states                                    打印状态机

环境:
  ACCOUNTPOOL_ROOT   仓库根，默认从可执行文件位置往上找
  ACCOUNTPOOL_DB     数据库路径，默认 <root>/backend/pool.db
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	root := findRoot()
	db, err := store.Open(dbPath(root))
	if err != nil {
		die("打开数据库: %v", err)
	}
	defer db.Close()
	st := store.New(db)

	switch os.Args[1] {
	case "add":
		cmdAdd(st, os.Args[2:])
	case "list", "ls":
		cmdList(st, os.Args[2:])
	case "show":
		cmdShow(st, os.Args[2:])
	case "run":
		cmdRun(st, root, os.Args[2:])
	case "admin-reset":
		cmdAdminReset(st)
	case "serve":
		cmdServe(st, os.Args[2:])
	case "retire":
		cmdRetire(st, root, os.Args[2:])
	case "maintain":
		cmdMaintain(st, root, os.Args[2:])
	case "worker":
		cmdWorker(st, root, os.Args[2:])
	case "job":
		cmdJob(st, os.Args[2:])
	case "proxy":
		cmdProxy(st, os.Args[2:])
	case "mail":
		cmdMail(os.Args[2:])
	case "states":
		fmt.Println("状态机（合法转移见 internal/pool/state.go）:")
		for _, s := range pool.AllStates() {
			mark := ""
			if pool.State(s).Terminal() {
				mark = "  [终态]"
			} else if pool.State(s).Usable() {
				mark = "  [可用]"
			}
			fmt.Printf("  %-20s%s\n", s, mark)
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
}

// splitArgs 把位置参数和 flag 分开。
//
// 为什么需要它：Go 标准库的 flag 包**遇到第一个非 flag 参数就停止解析**，
// 所以 `accountpool add claudetest --email x` 里的 --email 会被当成位置参数丢掉。
// 对命令行工具来说这个行为反直觉，我们自己拆一遍。
//
// valued 列出"需要跟一个值"的 flag 名，用来判断后面那个词是它的值还是位置参数。
func splitArgs(args []string, valued map[string]bool) (pos []string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		rest = append(rest, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue // --k=v 形式，自带值
		}
		if valued[name] && i+1 < len(args) {
			i++
			rest = append(rest, args[i])
		}
	}
	return
}

// cmdMail 是搬迁过来的邮箱操作。**它不碰 Claude，也不碰浏览器**——
// 之前这些在 Python 侧，每次调用都要起一个解释器进程，纯属浪费。
func cmdMail(args []string) {
	if len(args) < 1 {
		die("用法: accountpool mail list | accountpool mail new <name> | accountpool mail wait <jwt>")
	}
	mc := claude.NewMailClient()
	switch args[0] {
	case "list", "ls":
		addrs, err := mc.ListAddresses(100, 0)
		if err != nil {
			die("列地址: %v", err)
		}
		fmt.Printf("邮箱池（%s）共 %d 个:\n", mc.Domain, len(addrs))
		for _, a := range addrs {
			// 列表里的 name 就是完整地址
			fmt.Printf("  id=%-5d %-34s 收信 %d 封  建于 %s\n",
				a.ID, a.Name, a.MailCount, a.CreatedAt)
		}
	case "new":
		if len(args) < 2 {
			die("用法: accountpool mail new <name>")
		}
		a, err := mc.NewAddress(args[1])
		if err != nil {
			die("建地址: %v", err)
		}
		fmt.Printf("已建地址: %s (id=%d)\n", a.Address, a.AddressID)
		if a.JWT != "" {
			fmt.Println("  已拿到信箱 JWT（等邮件要用）")
		}
	case "wait":
		if len(args) < 2 {
			die("用法: accountpool mail wait <jwt> [--timeout 180]")
		}
		mail, err := mc.WaitForMail(args[1], 180*time.Second, 5*time.Second, nil)
		if err != nil {
			die("等邮件: %v", err)
		}
		fmt.Printf("收到邮件: %s\n", mail.Subject)
		if link := claude.ExtractMagicLink(mail); link != "" {
			fmt.Printf("  魔术链接: %s\n", short(link, 90))
		} else if code := claude.ExtractCode(mail); code != "" {
			fmt.Printf("  6 位验证码: %s\n", code)
		}
	default:
		die("未知 mail 子命令: %s", args[0])
	}
}

func cmdAdd(st *store.Store, args []string) {
	pos, rest := splitArgs(args, map[string]bool{"email": true, "proxy": true, "name": true})
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	email := fs.String("email", "", "邮箱地址")
	proxy := fs.String("proxy", "", "出口 URL")
	fs.Parse(rest)
	if len(pos) < 1 {
		die("用法: accountpool add <name> [--email X]")
	}
	name := pos[0]

	var proxyID int64
	if *proxy != "" {
		var err error
		proxyID, err = st.EnsureProxy(*proxy)
		if err != nil {
			die("出口配置无效: %v", err)
		}
	}
	id, err := st.CreateAccount(&store.Account{
		Name:       name,
		Email:      *email,
		BundlePath: filepath.Join("bundles", name),
		State:      string(pool.Pending),
	})
	if err != nil {
		die("建号失败: %v", err)
	}
	fmt.Printf("已建号 %s (id=%d) 状态=%s\n", name, id, presentation.State(string(pool.Pending)))
	if *email != "" {
		fmt.Printf("  邮箱: %s\n", *email)
	}
	if *proxy != "" {
		if err := st.BindAccountProxy(id, proxyID); err != nil {
			die("绑定出口: %v", err)
		}
		fmt.Printf("  已绑定出口 id=%d\n", proxyID)
	}
	fmt.Printf("  下一步: accountpool run %s --steps register\n", name)
}

func cmdList(st *store.Store, args []string) {
	_, rest := splitArgs(args, map[string]bool{"state": true})
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	state := fs.String("state", "", "只看某状态")
	fs.Parse(rest)

	var states []string
	if *state != "" {
		states = strings.Split(*state, ",")
	}
	accts, err := st.ListAccounts(states...)
	if err != nil {
		die("查号失败: %v", err)
	}
	if len(accts) == 0 {
		fmt.Println("（没有账号）")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "名字\t状态\t档位\t对话\t邮箱\t备注")
	for _, a := range accts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
			a.Name, presentation.State(a.State), orDash(a.Plan), a.WarmupCount,
			orDash(a.Email), presentation.Reason(a.LastError))
	}
	w.Flush()

	// 分组统计。DESIGN §7：**单一总数是错的**，要按状态分组看。
	tally := map[string]int{}
	for _, a := range accts {
		tally[a.State]++
	}
	fmt.Print("\n分组: ")
	first := true
	for _, s := range pool.AllStates() {
		if n, ok := tally[s]; ok {
			if !first {
				fmt.Print("  ")
			}
			fmt.Printf("%s=%d", presentation.State(s), n)
			first = false
		}
	}
	fmt.Println()
}

func cmdShow(st *store.Store, args []string) {
	if len(args) < 1 {
		die("用法: accountpool show <name>")
	}
	a, err := st.GetAccount(args[0])
	if err != nil {
		die("找不到 %s: %v", args[0], err)
	}
	fmt.Printf("名字     : %s (id=%d)\n", a.Name, a.ID)
	fmt.Printf("状态     : %s\n", presentation.State(a.State))
	fmt.Printf("邮箱     : %s\n", orDash(a.Email))
	fmt.Printf("org      : %s\n", orDash(a.OrgID))
	fmt.Printf("档位     : %s  tier=%s\n", orDash(a.Plan), orDash(a.RateLimitTier))
	fmt.Printf("bundle   : %s\n", orDash(a.BundlePath))
	fmt.Printf("对话     : %d 次，最后一次 %s\n", a.WarmupCount, orDash(a.LastWarmupAt))
	fmt.Printf("OAuth    : %s\n", hasToken(a))
	fmt.Printf("时间线   : 建 %s", a.CreatedAt)
	if a.RegisteredAt != "" {
		fmt.Printf("  注册 %s", a.RegisteredAt)
	}
	if a.SubscribedAt != "" {
		fmt.Printf("  订阅 %s", a.SubscribedAt)
	}
	if a.BannedAt != "" {
		fmt.Printf("  封禁 %s", a.BannedAt)
	}
	fmt.Println()
	if a.LastError != "" {
		fmt.Printf("最后错误 : %s\n", presentation.Reason(a.LastError))
	}

	evs, err := st.Events(a.ID, 30)
	if err != nil {
		return
	}
	if len(evs) > 0 {
		fmt.Println("\n事件（倒序）:")
		for _, e := range evs {
			trans := ""
			if e.FromState != "" || e.ToState != "" {
				trans = fmt.Sprintf(" %s→%s", presentation.State(e.FromState), presentation.State(e.ToState))
			}
			fmt.Printf("  %s  %-10s%s  %s\n", e.At, presentation.Event(e.Kind), trans, presentation.Reason(e.Detail))
		}
	}
}

func cmdRun(st *store.Store, root string, args []string) {
	pos, rest := splitArgs(args, map[string]bool{"steps": true})
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	steps := fs.String("steps", "register", "逗号分隔")
	fs.Parse(rest)
	if len(pos) < 1 {
		die("用法: accountpool run <name> --steps register,onboard")
	}
	name := pos[0]

	unlock, err := st.LockWorker()
	if err != nil {
		die("执行入口已占用: %v", err)
	}
	defer unlock()
	if err := runner.RecoverExecution(root); err != nil {
		die("恢复执行器: %v", err)
	}
	if _, err := st.RecoverInterruptedJobs(); err != nil {
		die("恢复中断任务: %v", err)
	}

	a, err := st.GetAccount(name)
	if err != nil {
		die("找不到 %s: %v", name, err)
	}

	if pool.State(a.State).Terminal() {
		die("账号已退役")
	}

	step := strings.Split(*steps, ",")[0]
	for _, s := range strings.Split(*steps, ",") {
		guess := guessState(s)
		if guess == "" {
			continue // 只读步骤（如 check）不改状态，没有可校验的目标
		}
		if err := pool.MustTransition(pool.State(a.State), guess); err != nil {
			// 不阻断，只提示——真实状态由执行侧结果决定
			fmt.Fprintf(os.Stderr, "提示: %v\n", err)
		}
	}

	r := runner.New(root)
	sch := &pool.Scheduler{}
	if err := runtimePolicy(st, sch, r)(); err != nil {
		die("读取执行配置: %v", err)
	}
	mc := r.Mail
	spec := runner.Spec{
		Platform: "claude",
		Account:  name,
		Steps:    strings.Split(*steps, ","),
		Options:  map[string]any{},
	}
	activity, err := st.ActivitySettings()
	if err != nil {
		die("%v", err)
	}
	prompt, err := store.RandomPrompt(activity.Prompts)
	if err != nil {
		die("%v", err)
	}
	spec.Options["prompt"] = prompt
	if reason, err := st.SchedulerPause(); err != nil {
		die("读取暂停状态: %v", err)
	} else if reason != "" {
		die("调度已暂停: %s；确认后使用 worker --resume", reason)
	}
	bound, err := st.AccountProxy(a.ID)
	if err != nil {
		die("读取出口: %v", err)
	}
	if os.Getenv("ACCOUNTPOOL_REQUIRE_PROXY") == "true" && bound == nil && *steps != "session" {
		die("账号尚未绑定代理，请先在代理池配置并测试出口")
	}
	if bound != nil {
		if bound.State != "available" || bound.CooldownUntil > time.Now().Format("2006-01-02 15:04:05") {
			die("绑定出口当前不可用")
		}
		held, err := st.AcquireProxy("", sql.NullInt64{Int64: bound.ID, Valid: true})
		if err != nil || held == nil {
			die("账号代理当前不可用，任务未启动")
		}
		defer st.UnlockProxy(held.ID)
		spec.Proxy = &held.URL
	}
	if a.Email != "" {
		spec.Email = &a.Email
	}
	// 注册需要收信 JWT。**邮箱是物料，归号池管**——服务端负责建、防撞名、
	// 记账、报废时删；执行侧只消费分配好的地址，不自己建。
	if strings.Contains(","+*steps+",", ",register,") {
		jwt, err := ensureMailbox(st, mc, a)
		if err != nil {
			die("准备邮箱: %v", err)
		}
		if jwt != "" {
			spec.MailJWT = jwt
		}
		if a.Email != "" {
			spec.Email = &a.Email
		}
	}
	if a.BundlePath == "" {
		bp := filepath.Join("bundles", name)
		spec.BundleRoot = &bp
	}

	if err := pool.PrepareStep(st, a, step); err != nil {
		die("记录执行状态: %v", err)
	}

	fmt.Printf("跑 %s: %s\n", name, presentation.Step(strings.Join(spec.Steps, ",")))
	jobID, err := st.StartManualJob(a.ID, *steps)
	if err != nil {
		die("记录任务: %v", err)
	}
	if bound != nil {
		if err := st.BindJobProxy(jobID, bound.ID); err != nil {
			die("记录任务代理失败")
		}
	}
	res, outPath, err := r.Run(context.Background(), spec)
	if err != nil {
		var unknown *runner.UnknownOutcomeError
		state, reason := "failed", "runner-error"
		if errors.As(err, &unknown) {
			state, reason = "blocked", "interrupted-unknown-outcome"
		}
		if finishErr := st.FinishJob(jobID, state, reason, err.Error(), outPath); finishErr != nil {
			die("记录中断失败: %v", finishErr)
		}
		if errors.As(err, &unknown) {
			if pauseErr := st.PauseScheduler("interrupted-unknown-outcome", time.Time{}); pauseErr != nil {
				die("记录暂停失败: %v", pauseErr)
			}
		}
		die("调用执行侧: %v", err)
	}

	fmt.Printf("  状态   : %s\n", presentation.State(res.Status))
	if res.FailedStep != "" {
		fmt.Printf("  停在   : %s\n", presentation.Step(res.FailedStep))
	}
	if res.Reason != "" {
		fmt.Printf("  原因   : %s\n", presentation.Reason(res.Reason))
	}
	fmt.Printf("  产物   : %s\n", outPath)

	for _, outcome := range res.Steps {
		if err := pool.ApplyOutcome(st, a, outcome.Step, outcome.Status, outcome.Reason, outcome.Credentials); err != nil {
			die("记录任务结果: %v", err)
		}
	}

	if reason, until, ok := pool.AccessPause(res.Reason, sch.BaseCooldown); ok {
		if err := st.PauseScheduler(reason, until); err != nil {
			die("记录暂停失败: %v", err)
		}
	}

	if err := st.FinishJob(jobID, res.Status, res.Reason, res.Detail, outPath); err != nil {
		die("结束任务: %v", err)
	}

	if res.Status == "ok" {
		if err := st.ClearAccountRetry(a.ID); err != nil {
			die("%v", err)
		}
	} else if pool.RetryableFailure(res.FailedStep, res.Reason, a) {
		retryStep := res.FailedStep
		if retryStep == "" {
			retryStep = step
		}
		if res.Reason == "blocked:session-expired" {
			retryStep = "register"
		}
		if err := st.ScheduleAccountRetry(a.ID, retryStep, res.Reason, sch.BaseCooldown); err != nil {
			die("%v", err)
		}
	}
	if res.Status != "ok" {
		os.Exit(1)
	}
}

// guessState 是"这一步跑起来会进入什么状态"的粗略推测，只用于跑前提示。
// 真实状态由执行侧的结果决定（pool.MappingFromRunner），这里不参与判定。
func guessState(step string) pool.State {
	switch step {
	case "register":
		return pool.Registering
	case "onboard":
		return pool.Registered
	case "warmup":
		return pool.Aging
	case "oauth":
		return pool.Subscribed
	}
	// check 是只读观测，不改状态，所以没有可推测的目标。
	// 返回空串让调用方跳过转移检查，免得报一条无意义的"非法转移"。
	return ""
}

func hasToken(a *store.Account) string {
	if a.AccessToken == "" {
		return "（无）"
	}
	if a.RefreshToken == "" {
		return "access 存在；refresh 缺失"
	}
	return "access / refresh 均存在"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func short(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func findRoot() string {
	if v := os.Getenv("ACCOUNTPOOL_ROOT"); v != "" {
		return v
	}
	// 默认：当前工作目录（服务端通常从仓库根启动）
	if wd, err := os.Getwd(); err == nil {
		if _, err := os.Stat(filepath.Join(wd, "executor")); err == nil {
			return wd
		}
	}
	// 退一步：可执行文件往上找
	exe, _ := os.Executable()
	d := filepath.Dir(exe)
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(d, "executor")); err == nil {
			return d
		}
		d = filepath.Dir(d)
	}
	wd, _ := os.Getwd()
	return wd
}

func dbPath(root string) string {
	if v := os.Getenv("ACCOUNTPOOL_DB"); v != "" {
		return v
	}
	return filepath.Join(root, "backend", "pool.db")
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "错误: "+f+"\n", a...)
	os.Exit(1)
}
