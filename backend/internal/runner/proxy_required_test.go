package runner

import (
	"context"
	"strings"
	"testing"
)

func TestMissingProxyStopsBeforeExecution(t *testing.T) {
	t.Setenv("ACCOUNTPOOL_REQUIRE_PROXY", "true")
	r := New(t.TempDir())
	r.Python = "/should-never-run"
	_, _, err := r.Run(context.Background(), Spec{Platform: "claude", Account: "fixture", Steps: []string{"register"}})
	if err == nil || !strings.Contains(err.Error(), "代理") {
		t.Fatalf("expected explicit proxy prerequisite: %v", err)
	}
}
