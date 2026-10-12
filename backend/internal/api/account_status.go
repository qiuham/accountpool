package api

import (
	"github.com/ham/accountpool/backend/internal/store"
)

func accountStatus(a store.MaintenanceAccount) (string, string) {
	if a.RetiredAt != "" || a.State == "retired" || a.State == "burned" {
		return "disabled", "账号已失效或退出维护池"
	}
	switch a.State {
	case "mailbox_create_failed":
		return "abnormal", "邮箱创建失败"
	case "mailbox_missing":
		return "abnormal", "邮箱缺失"
	case "mailbox_config_error":
		return "abnormal", "邮箱配置需要修复"
	case "manual", "needs_verification", "observing", "register_failed":
		return "abnormal", "账号需要恢复，请查看最近执行记录"
	case "login_required", "session_expired":
		return "login", "登录会话需要恢复"
	case "registering":
		if a.Jobs["register"].State != "running" {
			return "login", "登录尚未完成，请查看调度状态"
		}
		return "initializing", "正在登录 / 注册"
	case "pending":
		return "initializing", "正在准备账号或恢复登录"
	}
	if a.SessionKey == "" {
		return "login", "未取得有效登录会话"
	}
	reg, onboard, warm, check := a.Jobs["register"], a.Jobs["onboard"], a.Jobs["warmup"], a.LastCompletedCheck
	initialized := (onboard.State == "ok" && onboard.ID >= reg.ID) || (warm.State == "ok" && warm.ID >= reg.ID)
	if !initialized {
		return "initializing", "初始化尚未确认完成"
	}
	if check.ID == 0 || check.ID < reg.ID {
		return "initializing", "等待登录后的健康检查"
	}
	if check.State != "ok" {
		return "abnormal", "最近一次已完成的健康检查未通过"
	}
	return "normal", "最近一次健康检查通过"
}
