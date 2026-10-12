package pool

import (
	"strings"
	"time"
)

// AccessPause is shared by worker and manual operations; neither may bypass an
// upstream challenge by changing entrypoints.
func AccessPause(reason string, base time.Duration) (string, time.Time, bool) {
	if reason != "blocked:access-denied" && reason != "blocked:rate-limited" && !strings.HasPrefix(reason, "blocked:cf-challenge") {
		return "", time.Time{}, false
	}
	if base <= 0 {
		base = 30 * time.Minute
	}
	until := time.Now().Add(base)
	return "上游要求验证或限制访问，调度暂停: " + reason, until, true
}
