package presentation

import (
	"strings"
	"testing"
)

func TestChineseReasons(t *testing.T) {
	for _, s := range []string{"crashed-before-report", "blocked:cf-challenge-human-required", "scheduler-error: inspect task outcome before resume", "login-navigation-timeout", "mailbox-create-failed"} {
		got := Reason(s)
		if strings.Contains(got, "-") || got == "" {
			t.Fatalf("%s: %s", s, got)
		}
	}
	if Reason("future-error") != "执行异常，请查看执行记录" {
		t.Fatal("unknown code exposed")
	}
	if Step("register, check") != "登录 / 注册 → 健康检查" {
		t.Fatal(Step("register, check"))
	}
}
