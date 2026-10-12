// Package pool 是号池的领域逻辑：状态机 + 调度决策。
//
// 为什么状态机要放在服务端、且合法转移要写死
// ------------------------------------------------
// 执行侧是无状态的，它只回报"我看见了什么"（result.json 里的 status/reason），
// **该变成什么状态由服务端决定**。把转移表写死在这里，是为了让"跳到非法状态"
// 变成编译期/运行期就能发现的错误，而不是几个月后在数据里发现一堆乱七八糟的值。
package pool

import (
	"fmt"
	"sort"
)

// State 是账号生命周期状态。取值来自 DESIGN §8，另加两个实测发现。
type State string

const (
	// 注册链路
	LoginRequired       State = "login_required" // Existing account needs a fresh login.
	MailboxCreateFailed State = "mailbox_create_failed"
	MailboxMissing      State = "mailbox_missing"      // Address must be restored before login.
	MailboxConfigError  State = "mailbox_config_error" // Repair mailbox configuration before retry.
	Pending             State = "pending"              // 待注册（有邮箱，还没注册）
	Registering         State = "registering"          // 注册执行中
	Registered          State = "registered"           // 注册成功，未同步下游
	Synced              State = "synced"               // 已同步 sub2api
	RegisterFail        State = "register_failed"      // 注册失败（可换邮箱重试）

	// 养号链路
	Aging State = "aging" // 养号中
	Warm  State = "warm"  // 养号完成，可订阅

	// 订阅链路
	Subscribing State = "subscribing"
	Subscribed  State = "subscribed" // 已订阅（这时才可能换出 OAuth）
	Active      State = "active"     // 正常服务

	// 异常态
	Cooldown       State = "cooldown"           // 限流，临时不可调度（**不是封号**）
	Observing      State = "observing"          // 疑似受限，观察期
	Manual         State = "manual"             // 待人工介入（手机验证、复杂 captcha）
	SessionExpired State = "session_expired"    // 会话失效，可尝试重登（**可恢复**）
	NeedsVerify    State = "needs_verification" // 被验证门控拦住，收不到回复

	// 终态
	Burned State = "burned" // 确认死亡
)

// transitions 是合法转移表。**加转移必须先想清楚它对应什么业务事件。**
//
// 几个刻意不加的转移，都是有原因的：
//   - 任何状态 → burned 只允许从"确认死了"的状态过来，不许从 Cooldown 直接跳
//     （429 当封号是 DESIGN §6 点名的误杀重灾区）
//   - SessionExpired 可以回 Registering/Aging，因为它是**可恢复**的
//   - Burned 没有任何出边：墓碑是终态，防自杀循环靠的就是它不可逆
var transitions = map[State][]State{
	"": {Pending},

	LoginRequired:       {Registering, MailboxMissing, MailboxConfigError},
	MailboxCreateFailed: {Registering, LoginRequired},
	MailboxMissing:      {LoginRequired},
	MailboxConfigError:  {LoginRequired},
	Pending:             {SessionExpired, Registering, RegisterFail, Burned, Manual},

	Registering: {Registered, RegisterFail, SessionExpired, Cooldown, Manual,
		Burned /* 邮箱已被占用等确定性失败 */},

	RegisterFail: {Pending /* 换邮箱重试 */, Burned, Manual},

	Registered: {Subscribed, Synced, Aging, SessionExpired, Cooldown, Manual, Burned},

	Synced: {Subscribed, Aging, SessionExpired, Cooldown, Manual, Burned},

	Aging: {Subscribed, Warm, SessionExpired, Cooldown, Manual, NeedsVerify, Observing, Burned},

	Warm: {Subscribed, Subscribing, SessionExpired, Cooldown, Manual, NeedsVerify, Observing, Burned},

	Subscribing: {Subscribed, Warm /* 退订或失败回退 */, SessionExpired, Manual, Burned},

	Subscribed: {Active, SessionExpired, Cooldown, Manual, Observing, Burned},

	Active: {Cooldown, SessionExpired, Observing, Manual, Burned},

	// 异常态都可以回到正轨，或走向死亡
	Cooldown:       {Registering, Registered, Aging, Warm, Subscribed, Active, SessionExpired, Observing, Manual, Burned},
	Observing:      {Aging, Warm, Subscribed, Active, Manual, Burned},
	SessionExpired: {Registering, Aging, Warm, Subscribed, Active, Manual, Burned},
	NeedsVerify:    {Manual, Burned, Aging /* 验证补完可回到养号 */},
	Manual: {Pending, Registering, Aging, Warm, Subscribed, Active,
		SessionExpired, Burned},

	Burned: {}, // 终态，不可逆
}

// CanTransition 判转移是否合法。
func CanTransition(from, to State) bool {
	if from == to {
		return true // 同状态幂等，调多次不报错
	}
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// MustTransition 非法转移直接报错，附上合法目标方便排查。
func MustTransition(from, to State) error {
	if CanTransition(from, to) {
		return nil
	}
	return fmt.Errorf("非法状态转移 %s → %s（合法目标: %v）", from, to, transitions[from])
}

// Terminal 终态不可再变。
func (s State) Terminal() bool { return s == Burned }

// Usable 该状态下是否"这个号还能用"。路由到 sub2api 前的判断用它。
func (s State) Usable() bool {
	switch s {
	case Active, Subscribed, Warm, Aging:
		return true
	}
	return false
}

// NeedsVerifyReachable 判断某个状态是否可能是"被验证门控"导致的。
// 这个状态只能由养号时发消息发现（error_code: verification_required），
// **没有接口能提前查**——实测 kyc_status 覆盖不到它。
func (s State) NeedsVerifyReachable() bool { return s == Aging || s == Warm }

// MappingFromRunner 把执行侧回传的机器可判原因翻译成状态。
//
// 执行侧只搬运事实（"我看见了 verification_required"），
// **该变成什么状态由这里决定**——业务判断不该放进执行侧。
func MappingFromRunner(kind, status, reason string) (State, bool) {
	if status == "ok" {
		switch kind {
		case "register":
			return Registered, true
		case "onboard":
			return Registered, true
		case "warmup":
			return Aging, true
		case "check":
			return "", false // check 不改状态，只更新观测字段
		case "oauth":
			return Subscribed, true
		}
		return "", false
	}

	switch reason {
	case "blocked:session-expired":
		return SessionExpired, true
	case "blocked:cf-challenge-human-required":
		return Manual, true
	case "blocked:verification-required":
		return NeedsVerify, true

	case "blocked:cf-challenge-not-passed":
		// **同一个错误码，在不同阶段含义不同**：
		//   注册阶段 —— 还没有会话可言，挑战没过就是这次注册没成，换出口重试即可
		//   已有会话 —— 那多半是出口变了或会话被作废，走会话失效流程
		if kind == "register" {
			return RegisterFail, true
		}
		return SessionExpired, true

	case "blocked:banned":
		return Burned, true

	case "blocked:not-subscribed":
		return Manual, true // 换不出 token 说明还没订阅，停在"可订阅"

	case "blocked:browser-verification-failed", "login-session-not-established":
		if kind == "register" {
			return RegisterFail, true
		}
		return "", false

	case "mail-not-received", "failed:mail-timeout":
		return RegisterFail, true

	case "blocked:email-already-registered":
		return Burned, true // 这个邮箱被占用了，换一个

	case "email-box-missing":
		return Manual, true // 邮箱配置问题，不是号的问题，要人看
	}

	if len(reason) > 8 && reason[:8] == "blocked:" {
		return Manual, true // 其他 blocked 类需要人看
	}
	return "", false // failed 类不立刻改状态，交给重试逻辑
}

// AllStates 返回全部状态，供 CLI/文档生成。
func AllStates() []string {
	out := make([]string, 0, len(transitions))
	for s := range transitions {
		if s == "" {
			continue
		}
		out = append(out, string(s))
	}
	sort.Strings(out)
	return out
}
