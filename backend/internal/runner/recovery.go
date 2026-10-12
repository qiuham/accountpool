package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type executionRecord struct {
	OwnerPID   int    `json:"owner_pid"`
	OwnerStart string `json:"owner_start"`
	PID        int    `json:"pid"`
	Start      string `json:"start"`
	Boot       string `json:"boot"`
}

func processIdentity(pid int) (string, int, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", 0, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return "", 0, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return "", 0, fmt.Errorf("short process stat")
	}
	group, err := strconv.Atoi(fields[2])
	return fields[19], group, err
}
func bootIdentity() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(raw)), err
}
func journalPath(root string) string {
	return filepath.Join(root, "artifacts", "jobs", ".active-execution.json")
}
func recordExecution(root string, pid int) error {
	owner, _, err := processIdentity(os.Getpid())
	if err != nil {
		return err
	}
	start, group, err := processIdentity(pid)
	if err != nil {
		return err
	}
	if group != pid {
		return fmt.Errorf("executor lacks isolated process group")
	}
	boot, err := bootIdentity()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(executionRecord{os.Getpid(), owner, pid, start, boot})
	if err != nil {
		return err
	}
	path := journalPath(root)
	if err := os.WriteFile(path+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// RecoverExecution kills only a recorded group whose owner is gone and whose
// leader identity matches PID, kernel start time and boot ID. Unknown holders
// are left alone; their lease prevents another browser from starting.
func RecoverExecution(root string) error {
	jobs := filepath.Join(root, "artifacts", "jobs")
	if err := os.MkdirAll(jobs, 0700); err != nil {
		return err
	}
	raw, err := os.ReadFile(journalPath(root))
	hadJournal := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if hadJournal {
		var record executionRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("invalid execution journal: %w", err)
		}
		if record.PID <= 1 || record.OwnerPID <= 1 || record.Start == "" || record.OwnerStart == "" || record.Boot == "" {
			return fmt.Errorf("incomplete execution journal")
		}
		boot, err := bootIdentity()
		if err != nil {
			return err
		}
		if boot == record.Boot {
			owner, _, err := processIdentity(record.OwnerPID)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil && owner == record.OwnerStart {
				return fmt.Errorf("executor already running (recorded owner alive)")
			}
			start, group, err := processIdentity(record.PID)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil {
				if start != record.Start || group != record.PID {
					return fmt.Errorf("executor identity changed; refusing unrelated process cleanup")
				}
				if err := syscall.Kill(-record.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					return err
				}
			}
		}
	}
	lock, err := os.OpenFile(filepath.Join(jobs, ".executor.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !hadJournal || time.Now().After(deadline) {
			return fmt.Errorf("executor already running or unresolved orphan holds lock: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if hadJournal {
		if err := os.WriteFile(filepath.Join(jobs, ".last-finished"), []byte(time.Now().Format(time.RFC3339Nano)), 0600); err != nil {
			return err
		}
		return os.Remove(journalPath(root))
	}
	return nil
}

// UnknownOutcomeError prevents replay after a process started but produced no
// trustworthy result. It preserves errors.Is for cancellation and timeout.
type UnknownOutcomeError struct{ Err error }

func (e *UnknownOutcomeError) Error() string        { return "executor outcome unknown: " + e.Err.Error() }
func (e *UnknownOutcomeError) Unwrap() error        { return e.Err }
func (e *UnknownOutcomeError) OutcomeUnknown() bool { return true }
