package api

import (
	"github.com/ham/accountpool/backend/internal/presentation"
	"github.com/ham/accountpool/backend/internal/store"
	"time"
)

type activityTimer struct {
	Label   string `json:"label"`
	ReadyAt int64  `json:"readyAt"`
	Reason  string `json:"reason"`
}
type scheduleView struct {
	Check activityTimer `json:"check"`
	Chat  activityTimer `json:"chat"`

	Label      string `json:"label"`
	Reason     string `json:"reason"`
	ReadyAt    int64  `json:"readyAt"`
	ServerTime int64  `json:"serverTime"`
}

func accountScheduleSummary(a store.MaintenanceAccount, paused bool, now time.Time, registrationAt, proxyAt int64, checkInterval, chatInterval time.Duration) scheduleView {
	result := scheduleView{Label: "等待调度", Reason: "维护进程将在下一轮评估此账号", ServerTime: now.Unix()}
	for kind, j := range a.Jobs {
		if j.State == "running" {
			result.Label = "执行中"
			result.Reason = presentation.Step(kind)
			return result
		}
	}
	if a.RetiredAt != "" || a.State == "burned" || a.State == "retired" {
		result.Label = "已停止"
		result.Reason = "账号已退出维护"
		return result
	}
	if a.Paused || paused {
		result.Label = "已暂停"
		result.Reason = "恢复后继续按计划执行"
		return result
	}
	if a.AccessToken != "" || a.RefreshToken != "" {
		result.Label = "已移交"
		result.Reason = "授权账号由下游服务维护"
		return result
	}
	switch a.State {
	case "mailbox_missing", "mailbox_config_error", "manual", "needs_verification":
		result.Label = "执行受阻"
		result.Reason = "前置条件尚未满足，请查看账号状态说明"
		return result
	}
	step := a.RetryStep
	for kind, j := range a.Jobs {
		if j.State == "pending" {
			step = kind
			break
		}
	}
	if step != "" {
		result.Label = "排队中"
		result.Reason = presentation.Step(step) + "，等待串行执行器空闲"
		if a.RetryAt > now.Unix() {
			result.ReadyAt = a.RetryAt
			result.Reason = "失败重试等待"
		}
		if step == "register" && registrationAt > now.Unix() && registrationAt > result.ReadyAt {
			result.ReadyAt = registrationAt
			result.Reason = "平台注册 / 登录全局间隔"
		}
		if proxyAt > now.Unix() && proxyAt > result.ReadyAt {
			result.ReadyAt = proxyAt
			result.Reason = "绑定代理冷却"
		}
		if result.ReadyAt > 0 {
			result.Label = "等待冷却"
			if a.State == "mailbox_create_failed" && result.ReadyAt == a.RetryAt {
				result.Reason = "邮箱创建失败，等待重新准备邮箱"
			}
		}
		return result
	}
	if a.State == "registering" || a.State == "cooldown" || a.State == "register_failed" {
		result.Label = "执行受阻"
		result.Reason = presentation.Reason(a.LastError)
		if result.Reason == "" {
			result.Reason = "没有可执行的重试任务，请检查最近执行记录"
		}
		return result
	}
	check := a.Jobs["check"]
	if check.ID == 0 || a.Jobs["register"].ID > check.ID {
		if a.Jobs["onboard"].ID == 0 {
			result.Reason = "等待首次引导"
		} else {
			result.Reason = "等待登录后的健康检查"
		}
		return result
	}
	if t, e := time.ParseInLocation("2006-01-02 15:04:05", check.FinishedAt, time.Local); e == nil {
		result.ReadyAt = t.Add(checkInterval).Unix()
		result.Label = "等待检查"
		result.Reason = "下一次健康检查"
	}
	// Match the controller: chat only follows successful check and initialization.
	initialized := a.Jobs["onboard"].State == "ok" || (a.Jobs["warmup"].State == "ok" && a.Jobs["warmup"].ID >= a.Jobs["register"].ID)
	if chatInterval >= time.Hour && a.SessionKey != "" && a.Plan == "claude_free" && check.State == "ok" && initialized {
		j, exists := a.Jobs["warmup"]
		if !exists {
			result.ReadyAt = 0
			result.Label = "等待调度"
			result.Reason = "首次定时对话"
		} else if t, e := time.ParseInLocation("2006-01-02 15:04:05", j.FinishedAt, time.Local); e == nil && j.State == "ok" {
			due := t.Add(chatInterval).Unix()
			if result.ReadyAt == 0 || due < result.ReadyAt {
				result.ReadyAt = due
				result.Label = "等待对话"
				result.Reason = "下一次定时对话"
			}
		}
	}
	if result.ReadyAt != 0 && result.ReadyAt <= now.Unix() {
		result.ReadyAt = 0
		result.Label = "等待调度"
		result.Reason += "已到期，等待维护进程安排"
	}
	return result
}

// accountSchedule preserves both independent clocks; the summary only selects
// the earliest eligible action and must not hide the other periodic task.
func accountSchedule(a store.MaintenanceAccount, paused bool, now time.Time, registrationAt, proxyAt int64, checkInterval, chatInterval time.Duration) scheduleView {
	s := accountScheduleSummary(a, paused, now, registrationAt, proxyAt, checkInterval, chatInterval)
	s.Check = activityClock(a, "check", paused, now, proxyAt, checkInterval)
	s.Chat = activityClock(a, "warmup", paused, now, proxyAt, chatInterval)
	return s
}
func activityClock(a store.MaintenanceAccount, step string, paused bool, now time.Time, proxyAt int64, interval time.Duration) activityTimer {
	result := activityTimer{Label: "待执行"}
	if step == "warmup" && interval < time.Hour {
		result.Label = "未启用"
		result.Reason = "定时对话尚未开启"
		return result
	}
	j, exists := a.Jobs[step]
	if j.State == "running" {
		result.Label = "执行中"
		return result
	}
	if a.RetiredAt != "" || a.State == "burned" || a.State == "retired" {
		result.Label = "已停止"
		return result
	}
	if a.Paused || paused {
		result.Label = "已暂停"
		result.Reason = "恢复维护后按原计划继续"
		return result
	}
	if a.AccessToken != "" || a.RefreshToken != "" || (step == "warmup" && a.Plan != "" && a.Plan != "claude_free") {
		result.Label = "不适用"
		result.Reason = "不参与免费账号定时维护"
		return result
	}
	if a.RetryStep == step {
		result.Label = "重试"
		result.ReadyAt = max(a.RetryAt, proxyAt)
		result.Reason = "失败重试等待，不是正常执行周期"
		if result.ReadyAt <= now.Unix() {
			result.Label = "等待重试"
			result.ReadyAt = 0
		}
		return result
	}
	if j.State == "pending" {
		result.Label = "已排队"
		result.Reason = "等待串行执行器空闲"
		return result
	}
	if a.SessionKey == "" || a.State == "session_expired" || a.State == "login_required" {
		result.Label = "待登录"
		return result
	}
	initialized := a.Jobs["onboard"].State == "ok" || (a.Jobs["warmup"].State == "ok" && a.Jobs["warmup"].ID >= a.Jobs["register"].ID)
	if !initialized {
		result.Label = "待初始化"
		return result
	}
	if step == "warmup" && a.Jobs["check"].State != "ok" {
		result.Label = "待检查"
		result.Reason = "健康检查通过后继续"
		return result
	}
	if exists && step == "warmup" && j.State != "ok" && !(j.State == "failed" && a.Jobs["check"].ID > j.ID) {
		result.Label = "需检查"
		result.Reason = presentation.Reason(j.Reason)
		return result
	}
	if !exists {
		result.Label = "待首次执行"
		return result
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", j.FinishedAt, time.Local)
	if err != nil {
		result.Label = "时间待确认"
		return result
	}
	result.ReadyAt = t.Add(interval).Unix()
	result.Label = "周期"
	result.Reason = "按上次任务完成时间计算；到期后等待调度"
	if result.ReadyAt <= now.Unix() {
		result.Label = "已到期"
		result.ReadyAt = 0
	}
	return result
}
