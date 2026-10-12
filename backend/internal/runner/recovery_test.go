package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCrashOwnerHelper(t *testing.T) {
	if os.Getenv("RUNNER_CRASH_FIXTURE") != "1" {
		return
	}
	r := &Runner{ProjectRoot: os.Getenv("RUNNER_FIXTURE_ROOT"), Python: os.Getenv("RUNNER_FIXTURE_PYTHON")}
	_, _, _ = r.Run(context.Background(), fixtureSpec())
	os.Exit(0)
}
func TestCrashRecoveryReleasesOrphanExecutorLease(t *testing.T) {
	r := fixtureRunner(t, "")
	if err := os.WriteFile(r.Python, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	owner := exec.Command(os.Args[0], "-test.run=^TestCrashOwnerHelper$")
	owner.Env = append(os.Environ(), "RUNNER_CRASH_FIXTURE=1", "RUNNER_FIXTURE_ROOT="+r.ProjectRoot, "RUNNER_FIXTURE_PYTHON="+r.Python)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer owner.Process.Kill()
	var record executionRecord
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(journalPath(r.ProjectRoot))
		if err == nil && json.Unmarshal(raw, &record) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not launch executor")
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer syscall.Kill(-record.PID, syscall.SIGKILL)
	if err := RecoverExecution(r.ProjectRoot); err == nil {
		t.Fatal("live owner's executor cleaned up")
	}
	owner.Process.Kill()
	owner.Wait()
	f, err := os.OpenFile(filepath.Join(r.ProjectRoot, "artifacts/jobs/.executor.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("fixture orphan did not retain lease")
	}
	if err := RecoverExecution(r.ProjectRoot); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal("orphan lease not released")
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if _, err := os.Stat(journalPath(r.ProjectRoot)); !os.IsNotExist(err) {
		t.Fatal("stale execution journal retained")
	}
	if _, err := os.Stat(filepath.Join(r.ProjectRoot, "artifacts/jobs/.last-finished")); err != nil {
		t.Fatal("recovery did not record cooldown")
	}
}
func TestRecoveryRejectsChangedProcessIdentity(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "artifacts/jobs"), 0700)
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	if err := recordExecution(root, child.Process.Pid); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(journalPath(root))
	var record executionRecord
	json.Unmarshal(raw, &record)
	record.OwnerStart = "reused-owner"
	record.Start = "reused-child"
	raw, _ = json.Marshal(record)
	os.WriteFile(journalPath(root), raw, 0600)
	if err := RecoverExecution(root); err == nil {
		t.Fatal("changed identity was accepted")
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("unrelated child was terminated")
	}
}
