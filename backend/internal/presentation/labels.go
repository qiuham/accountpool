// Package presentation provides Chinese labels without changing persisted machine codes.
package presentation

import (
	"strings"
	"unicode"
)

var reasons = map[string]string{
	"proxy-required":                 "未分配账号代理，任务尚未执行",
	"credential-missing":             "缺少登录凭据",
	"http-operation-failed":          "服务请求失败",
	"profile-request-failed":         "账号资料读取失败",
	"unknown-account-health":         "账号健康状态尚未确认",
	"verification-timeout":           "等待验证超时",
	"mailbox-read-failed":            "邮件读取失败",
	"mailbox-invalid-sequence":       "邮箱操作顺序异常",
	"mailbox-invalid-timeout":        "邮箱等待时间配置无效",
	"mailbox-snapshot-already-taken": "邮箱收信快照已记录",
	"mailbox-unknown-operation":      "不支持此邮箱操作",
	"mailbox-restored":               "邮箱已恢复",

	"crashed-before-report":            "执行器提前退出，未生成结果报告",
	"login-navigation-timeout":         "登录链接跳转超时",
	"login-navigation-error":           "登录页面打开失败",
	"login-session-not-established":    "登录未完成，尚未取得有效会话",
	"mailbox-create-failed":            "邮箱创建失败",
	"mailbox-prerequisite-failed":      "邮箱配置或凭据异常",
	"mailbox-prerequisite-unresolved":  "邮箱尚未准备就绪",
	"mailbox-blocked":                  "邮箱服务异常",
	"scheduler-error":                  "调度异常，请检查最近执行记录后恢复",
	"operator-paused":                  "手动暂停",
	"interrupted-unknown-outcome":      "任务中断，执行结果尚未确认",
	"access-denied":                    "访问被拒绝",
	"browser-verification-failed":      "浏览器验证失败",
	"cf-challenge":                     "等待浏览器验证",
	"cf-challenge-human-required":      "浏览器验证需要人工确认",
	"cf-challenge-not-passed":          "浏览器验证未通过",
	"email-already-registered":         "邮箱已被注册",
	"no-bundle":                        "浏览器资料未找到",
	"no-session-key":                   "未取得登录会话",
	"not-subscribed":                   "账号尚未订阅",
	"rate-limited":                     "请求过于频繁，请稍后重试",
	"session-expired":                  "登录会话已失效",
	"verification-required":            "账号需要验证",
	"banned":                           "账号已被停用",
	"bad-report":                       "执行结果报告格式异常",
	"personal-confirmation-required":   "初始化需要本人确认",
	"onboarding-required":              "需要完成初始化",
	"tool-permission-pending":          "工具授权未完成",
	"missing-chat-input":               "未找到对话输入框",
	"no-artifacts":                     "执行器未生成结果文件",
	"executor-memory-pressure":         "执行器内存不足，稍后自动恢复",
	"runner-error":                     "执行器启动或运行失败",
	"mail-not-received":                "未收到登录邮件",
	"mail-timeout":                     "等待登录邮件超时",
	"email-box-missing":                "邮箱不存在",
	"email-not-entered":                "邮箱输入未完成",
	"navigation-error":                 "页面打开失败",
	"navigation-timeout":               "页面加载超时",
	"page-http-error":                  "页面服务返回错误",
	"http-error":                       "服务请求失败",
	"page-not-ready":                   "页面尚未就绪",
	"spa-not-rendered":                 "页面内容尚未加载",
	"unexpected-page":                  "页面与预期不符",
	"completion-error":                 "对话生成失败",
	"completion-http-error":            "对话服务返回错误",
	"completion-invalid-event":         "对话事件格式异常",
	"completion-invalid-json":          "对话数据格式异常",
	"completion-missing-response":      "未收到对话响应",
	"completion-missing-terminal":      "未收到对话完成标记",
	"completion-network-error":         "对话连接异常",
	"completion-rate-limited":          "对话请求受到限流",
	"completion-response-unreadable":   "对话响应读取失败",
	"completion-stream-error":          "对话传输中断",
	"completion-unexpected-response":   "对话响应与预期不符",
	"completion-verification-required": "对话需要账号验证",
	"reply-not-confirmed":              "尚未确认收到完整回复",
	"send-failed":                      "消息发送失败",
	"onboarding-action-no-progress":    "初始化操作未取得进展",
	"onboarding-incomplete":            "账号初始化未完成",
	"unsupported-step":                 "不支持此执行步骤",
	"account-retired":                  "账号已退出维护",
	"oauth-account-excluded":           "已授权账号不参与免费账号维护",
	"paid-account-excluded":            "订阅账号不参与免费账号维护",
	"scheduled-chat-disabled":          "定时对话已关闭",
	"mailbox:address-not-found":        "邮箱不存在",
	"mailbox:admin-auth-missing":       "未配置邮箱管理员凭据",
	"mailbox:address-mismatch":         "邮箱地址与配置不一致",
}
var states = map[string]string{
	"pending":               "等待执行",
	"running":               "执行中",
	"ok":                    "已完成",
	"failed":                "执行失败",
	"blocked":               "执行受阻",
	"cancelled":             "已取消",
	"login_required":        "待登录",
	"mailbox_missing":       "邮箱缺失",
	"mailbox_config_error":  "邮箱配置异常",
	"mailbox_create_failed": "邮箱创建失败",
	"registering":           "登录 / 注册中",
	"registered":            "已注册",
	"synced":                "已同步",
	"register_failed":       "注册失败",
	"aging":                 "维护中",
	"warm":                  "维护中",
	"subscribing":           "订阅中",
	"subscribed":            "已订阅",
	"active":                "正常",
	"cooldown":              "等待重试",
	"observing":             "观察中",
	"manual":                "等待处理",
	"session_expired":       "会话失效",
	"needs_verification":    "需要验证",
	"burned":                "已失效",
}
var steps = map[string]string{
	"register": "登录 / 注册",
	"onboard":  "首次引导",
	"warmup":   "网页对话",
	"session":  "获取会话",
	"check":    "健康检查",
	"oauth":    "获取授权",
}

func Reason(value string) string {
	if value == "" {
		return ""
	}
	value = strings.TrimSpace(value)
	if text, ok := reasons[value]; ok {
		return text
	}
	code := strings.TrimPrefix(strings.TrimPrefix(value, "blocked:"), "failed:")
	if text, ok := reasons[code]; ok {
		return text
	}
	if strings.HasPrefix(code, "scheduler-error:") {
		return reasons["scheduler-error"]
	}
	if strings.HasPrefix(code, "mailbox-") || strings.HasPrefix(code, "mailbox:") {
		return "邮箱服务异常，请检查邮箱配置和执行记录"
	}
	// Existing Chinese validation messages can pass through; raw technical diagnostics cannot.
	if strings.IndexFunc(value, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 && !strings.Contains(value, "://") && !strings.Contains(value, "-") {
		return value
	}
	return "执行异常，请查看执行记录"
}
func State(value string) string {
	if text, ok := states[value]; ok {
		return text
	}
	return "未知状态"
}
func Step(value string) string {
	parts := strings.Split(value, ",")
	for i, p := range parts {
		if s, ok := steps[strings.TrimSpace(p)]; ok {
			parts[i] = s
		} else {
			parts[i] = "未知步骤"
		}
	}
	return strings.Join(parts, " → ")
}

func Event(value string) string {
	switch value {
	case "retry-scheduled":
		return "安排重试"
	case "mailbox-blocked":
		return "邮箱异常"
	case "created":
		return "创建账号"
	case "registered":
		return "注册完成"
	case "retired":
		return "退出维护"
	case "register", "onboard", "warmup", "check", "oauth":
		return Step(value)
	default:
		return "状态更新"
	}
}
