package runner

import (
	"context"
	"testing"
)

// Run in a 512 MiB container: an inexpensive task must not be rejected just
// because the container cannot reserve the former browser memory budget.
func TestExecutionNotGatedByMemory(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"fixture","status":"ok"}`)
	res, _, err := r.Run(context.Background(), fixtureSpec())
	if err != nil {
		t.Fatalf("task rejected before execution: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status=%s", res.Status)
	}
}
