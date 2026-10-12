package runner

import (
	"context"
	"testing"
)

func TestProxyIsOptionalByDefault(t *testing.T) {
	t.Setenv("ACCOUNTPOOL_REQUIRE_PROXY", "")
	r := fixtureRunner(t, `{"platform":"claude","account":"fixture","status":"ok"}`)
	spec := fixtureSpec()
	spec.Proxy = nil
	res, _, e := r.Run(context.Background(), spec)
	if e != nil || res.Status != "ok" {
		t.Fatalf("optional proxy rejected: %v", e)
	}
}
