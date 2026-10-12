package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSecondExecutorRejectedWhileLockHeld(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"fixture","status":"ok"}`)
	root := filepath.Join(r.ProjectRoot, "artifacts", "jobs")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(root, ".executor.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if _, _, err = r.Run(context.Background(), fixtureSpec()); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("lock not enforced: %v", err)
	}
}
func TestSequentialCooldown(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"fixture","status":"ok"}`)
	r.Cooldown = 40 * time.Millisecond
	if _, _, err := r.Run(context.Background(), fixtureSpec()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, _, err := r.Run(context.Background(), fixtureSpec()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < r.Cooldown-5*time.Millisecond {
		t.Fatal("cooldown skipped")
	}
}
func TestCooldownCanceledBeforeLaunch(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"fixture","status":"ok"}`)
	r.Cooldown = time.Minute
	root := filepath.Join(r.ProjectRoot, "artifacts", "jobs")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".last-finished"), []byte(time.Now().Format(time.RFC3339Nano)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := r.Run(ctx, fixtureSpec()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel not propagated: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, "fixture-*"))
	if len(matches) != 0 {
		t.Fatal("executor launched during cooldown")
	}
}
func TestBlockedResultAcceptedWithNonzeroExit(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"fixture","status":"blocked","reason":"blocked:cf-challenge-human-required"}`)
	f, err := os.OpenFile(r.Python, os.O_APPEND|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("exit 2\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	res, path, err := r.Run(context.Background(), fixtureSpec())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "blocked" {
		t.Fatal("lost blocked outcome")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "job.json")); !os.IsNotExist(err) {
		t.Fatal("sensitive job input retained")
	}
}
func TestFailureIsNotChangedIntoSuccess(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"fixture","status":"ok"}`)
	f, err := os.OpenFile(r.Python, os.O_APPEND|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("exit 1\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.Run(context.Background(), fixtureSpec()); err == nil {
		t.Fatal("failed process reported success")
	}
}
func TestMissingResultRetainsLog(t *testing.T) {
	r := fixtureRunner(t, "")
	if err := os.WriteFile(r.Python, []byte("#!/bin/sh\necho fixture-executor-error\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, path, err := r.Run(context.Background(), fixtureSpec())
	if err == nil {
		t.Fatal("missing result accepted")
	}
	raw, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), "executor.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(raw), "fixture-executor-error") {
		t.Fatal("diagnostic log lost")
	}
}

func TestTimeoutTerminatesChildProcess(t *testing.T) {
	r := fixtureRunner(t, "")
	r.Timeout = 60 * time.Millisecond
	pidFile := filepath.Join(r.ProjectRoot, "child.pid")
	script := "#!/bin/sh\nsleep 30 &\necho $! > '" + pidFile + "'\nwait\n"
	if err := os.WriteFile(r.Python, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Run(context.Background(), fixtureSpec()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout not propagated: %v", err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	statPath := filepath.Join("/proc", strings.TrimSpace(string(raw)), "stat")
	for i := 0; i < 20; i++ {
		stat, err := os.ReadFile(statPath)
		if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(stat))
		if len(fields) > 2 && (fields[2] == "Z" || fields[2] == "X") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("executor child still running after cancellation")
}
