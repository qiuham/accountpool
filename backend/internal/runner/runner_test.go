package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixtureRunner(t *testing.T, output string) *Runner {
	t.Helper()
	root := t.TempDir()
	script := filepath.Join(root, "fixture-executor")
	body := "#!/bin/sh\nprintf '%s' '" + output + "' > \"$7\"\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return &Runner{ProjectRoot: root, Python: script, Timeout: time.Second}
}
func fixtureSpec() Spec {
	proxy := "http://127.0.0.1:9" // No requests: execution is mocked.
	return Spec{Proxy: &proxy, Platform: "claude", Account: "fixture", Steps: []string{"warmup"}}
}
func TestResultPersists(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"fixture","status":"ok"}`)
	_, path, err := r.Run(context.Background(), fixtureSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("returned result path already deleted")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("result permissions = %o", info.Mode().Perm())
	}
}
func TestRejectInvalidStatus(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"fixture","status":"garbage"}`)
	if _, _, err := r.Run(context.Background(), fixtureSpec()); err == nil {
		t.Fatal("invalid result status accepted")
	}
}
func TestRejectWrongAccount(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"other","status":"ok"}`)
	if _, _, err := r.Run(context.Background(), fixtureSpec()); err == nil {
		t.Fatal("wrong result account accepted")
	}
}
func TestRejectPathTraversal(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"../escape","status":"ok"}`)
	spec := fixtureSpec()
	spec.Account = "../escape"
	if _, _, err := r.Run(context.Background(), spec); err == nil {
		t.Fatal("account path traversal accepted")
	}
}
