// Package runner executes native HTTP and browser steps under one process lease.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/nettrace"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Spec 是喂给执行侧的输入。字段名必须与 JobSpec 严格一致。
type Spec struct {
	Platform     string         `json:"platform"`
	Account      string         `json:"account"`
	Steps        []string       `json:"steps"`
	Email        *string        `json:"email,omitempty"`
	Proxy        *string        `json:"proxy,omitempty"`
	Fingerprint  map[string]any `json:"fingerprint,omitempty"`
	Engine       *string        `json:"engine,omitempty"`
	Name         *string        `json:"name,omitempty"`
	BundleRoot   *string        `json:"bundle_root,omitempty"`
	ArtifactRoot *string        `json:"artifact_root,omitempty"`
	Options      map[string]any `json:"options,omitempty"`
	MailJWT      string         `json:"-"`
}

// Credentials 是执行侧产出的凭据。
type Credentials struct {
	SessionKey string `json:"sessionKey"`
	OrgID      string `json:"orgId"`
	// 执行侧会往 extra 里塞别的键，保留原始 JSON 以便取用
	Extra map[string]any `json:"-"`
}

// Result 是执行侧的回报。字段名与 JobResult 严格一致。
type Result struct {
	Platform   string          `json:"platform"`
	Account    string          `json:"account"`
	Status     string          `json:"status"` // ok / failed / blocked
	Creds      json.RawMessage `json:"credentials"`
	Email      string          `json:"email"`
	FailedStep string          `json:"failed_step"`
	Reason     string          `json:"reason"`
	Detail     string          `json:"detail"`
	Artifacts  string          `json:"artifacts"`
	StartedAt  string          `json:"started_at"`
	Steps      []StepOutcome   `json:"steps,omitempty"`
	FinishedAt string          `json:"finished_at"`
}

// Credentials 解开 credentials 字段。执行侧不同平台字段不同，所以先收原始 JSON。
func (r *Result) CredsOf() Credentials {
	var c Credentials
	if len(r.Creds) == 0 {
		return c
	}
	var m map[string]any
	if err := json.Unmarshal(r.Creds, &m); err != nil {
		return c
	}
	if v, ok := m["sessionKey"].(string); ok {
		c.SessionKey = v
	}
	if v, ok := m["orgId"].(string); ok {
		c.OrgID = v
	}
	c.Extra = m
	return c
}

// Runner 调执行侧。ProjectRoot 是仓库根（executor/ 和 bundles/ 都在它下面）。
type Runner struct {
	Trace       nettrace.Capture
	TraceFile   string
	ProjectRoot string
	Client      *claude.Client // Injected only by local protocol tests.
	Mail        *claude.MailClient
	Python      string        // 默认 <root>/.venv-stable/bin/python
	Timeout     time.Duration // 单次任务上限；0 表示不限
	Cooldown    time.Duration // 全局串行调用的最小冷却时间
}

func New(projectRoot string) *Runner {
	py := filepath.Join(projectRoot, ".venv-stable", "bin", "python")
	return &Runner{ProjectRoot: projectRoot, Python: py, Mail: claude.NewMailClient(), Timeout: 30 * time.Minute, Cooldown: time.Minute}
}

// Run 跑一个任务。返回 Result 与产物路径。
//
// **注意它不判"该不该重试"**——status 和 reason 原样交回，决策在 pool 层。
var accountName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type StepOutcome struct {
	Step        string         `json:"step"`
	Status      string         `json:"status"`
	Reason      string         `json:"reason,omitempty"`
	Credentials map[string]any `json:"credentials,omitempty"`
}

func (r *Runner) Run(ctx context.Context, spec Spec) (result *Result, resultPath string, resultErr error) {
	if !accountName.MatchString(spec.Account) || spec.Platform != "claude" {
		return nil, "", fmt.Errorf("invalid job identity")
	}
	if len(spec.Steps) == 0 {
		return nil, "", fmt.Errorf("empty steps")
	}
	for _, step := range spec.Steps {
		switch step {
		case "register", "onboard", "warmup", "session", "check", "oauth":
		default:
			return nil, "", fmt.Errorf("unsupported step: %s", step)
		}
	}
	needsNetwork := false
	for _, step := range spec.Steps {
		if step != "session" {
			needsNetwork = true
		}
	}
	if os.Getenv("ACCOUNTPOOL_REQUIRE_PROXY") == "true" && needsNetwork && (spec.Proxy == nil || strings.TrimSpace(*spec.Proxy) == "") {
		return nil, "", fmt.Errorf("未分配账号代理，已阻止访问 Claude")
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	err := r.WithLock(ctx, func(lock *os.File) error {
		workDir, err := os.MkdirTemp(filepath.Join(r.ProjectRoot, "artifacts", "jobs"), spec.Account+"-*")
		if err != nil {
			return err
		}
		resultPath = filepath.Join(workDir, "result.json")
		result = &Result{Platform: spec.Platform, Account: spec.Account, Status: "ok", StartedAt: time.Now().Format(time.RFC3339)}
		aggregate := map[string]any{}
		for index, step := range spec.Steps {
			stepDir := filepath.Join(workDir, fmt.Sprintf("%02d-%s", index, step))
			if err := os.Mkdir(stepDir, 0700); err != nil {
				return err
			}
			var current *Result
			if step == "check" || step == "oauth" {
				sessionSpec := spec
				sessionSpec.Steps = []string{"session"}
				sessionResult, err := r.browser(ctx, sessionSpec, stepDir, lock)
				if err != nil {
					return err
				}
				if sessionResult.Status != "ok" {
					current = sessionResult
					current.FailedStep = step
				} else {
					current, err = r.native(ctx, spec, step, sessionResult.CredsOf())
					if err != nil {
						return err
					}
				}
			} else {
				single := spec
				single.Steps = []string{step}
				current, err = r.browser(ctx, single, stepDir, lock)
				if err != nil {
					return err
				}
			}
			values := current.CredsOf().Extra
			if step == "register" && current.Status == "ok" {
				if values == nil {
					values = map[string]any{}
				}
				profile, profileErr := r.native(ctx, spec, "profile", current.CredsOf())
				if profileErr == nil && profile.Status == "ok" {
					for key, value := range profile.CredsOf().Extra {
						values[key] = value
					}
					values["metadataStatus"] = "ok"
				} else {
					// Login already succeeded. Record metadata failure independently; do not
					// discard valid credentials or pretend a health check happened.
					values["metadataStatus"] = "failed"
					values["metadataReason"] = "profile-request-failed"
					if profile != nil && profile.Reason != "" {
						values["metadataReason"] = profile.Reason
					}
				}
			}
			for k, v := range values {
				aggregate[k] = v
			}
			result.Steps = append(result.Steps, StepOutcome{Step: step, Status: current.Status, Reason: current.Reason, Credentials: values})
			if current.Artifacts != "" {
				result.Artifacts = current.Artifacts
			}
			if current.Status != "ok" {
				result.Status = current.Status
				result.Reason = current.Reason
				result.Detail = current.Detail
				result.FailedStep = step
				break
			}
		}
		result.Creds, _ = json.Marshal(aggregate)
		result.FinishedAt = time.Now().Format(time.RFC3339)
		raw, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(resultPath, raw, 0600)
	})
	if err != nil {
		return result, resultPath, err
	}
	return result, resultPath, nil
}

// WithLock covers native HTTP, browser IPC and retirement with one execution
// lease and cooldown. Independent Python uses the same inherited lock.
func (r *Runner) WithLock(ctx context.Context, fn func(*os.File) error) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := RecoverExecution(r.ProjectRoot); err != nil {
		return err
	}
	jobs := filepath.Join(r.ProjectRoot, "artifacts", "jobs")
	if err := os.MkdirAll(jobs, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(jobs, ".executor.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("executor already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	finishedPath := filepath.Join(jobs, ".last-finished")
	if r.Cooldown > 0 {
		raw, err := os.ReadFile(finishedPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			finished, err := time.Parse(time.RFC3339Nano, string(raw))
			if err != nil {
				return fmt.Errorf("invalid cooldown record")
			}
			if wait := time.Until(finished.Add(r.Cooldown)); wait > 0 {
				timer := time.NewTimer(wait)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
	}
	defer func() {
		if err := os.WriteFile(finishedPath, []byte(time.Now().Format(time.RFC3339Nano)), 0600); err != nil && resultErr == nil {
			resultErr = &UnknownOutcomeError{Err: err}
		}
	}()
	return fn(lock)
}

func (r *Runner) native(ctx context.Context, spec Spec, step string, session Credentials) (*Result, error) {
	result := &Result{Platform: spec.Platform, Account: spec.Account, Status: "ok"}
	client := r.Client
	if client == nil {
		proxy := ""
		if spec.Proxy != nil {
			proxy = *spec.Proxy
		}
		var err error
		client, err = claude.NewClient(proxy)
		if err != nil {
			return nil, err
		}
		defer client.HTTP.CloseIdleConnections()
	}
	previousTrace := client.Trace
	client.Trace = r.Trace
	defer func() { client.Trace = previousTrace }()
	var values map[string]any
	var err error
	if step == "profile" {
		values, err = client.Profile(ctx, session.SessionKey, session.OrgID)
	} else if step == "oauth" {
		values, err = client.Authorize(ctx, session.SessionKey, session.OrgID)
	} else {
		values, err = client.Check(ctx, session.SessionKey, session.OrgID)
	}
	if err != nil {
		var uncertain interface{ OutcomeUnknown() bool }
		if errors.As(err, &uncertain) && uncertain.OutcomeUnknown() {
			return nil, &UnknownOutcomeError{Err: err}
		}
		if ctx.Err() != nil {
			return nil, &UnknownOutcomeError{Err: ctx.Err()}
		}
		result.Status = "failed"
		result.Reason = "http-operation-failed"
		var apiError *claude.APIError
		if errors.As(err, &apiError) {
			result.Reason = apiError.Reason
		}
		if strings.HasPrefix(result.Reason, "blocked:") {
			result.Status = "blocked"
		}
		result.FailedStep = step
	} else if step == "check" {
		health, _ := values["health"].(string)
		switch health {
		case "ok", "not_subscribed":
		case "unknown":
			result.Status = "failed"
			result.Reason = "unknown-account-health"
		default:
			result.Status = "blocked"
			result.Reason = "blocked:" + strings.ReplaceAll(health, "_", "-")
		}
	}
	if values == nil {
		values = map[string]any{}
	}
	values["sessionKey"] = session.SessionKey
	result.Creds, _ = json.Marshal(values)
	return result, nil
}

func (r *Runner) browser(ctx context.Context, spec Spec, workDir string, lock *os.File) (result *Result, resultErr error) {
	started := false
	defer func() {
		if started && resultErr != nil {
			resultErr = &UnknownOutcomeError{Err: resultErr}
		}
	}()
	jobPath := filepath.Join(workDir, "job.json")
	outPath := filepath.Join(workDir, "result.json")
	buf, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(jobPath, buf, 0600); err != nil {
		return nil, err
	}
	defer os.Remove(jobPath)
	log, err := os.OpenFile(filepath.Join(filepath.Dir(workDir), "executor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	cmd := exec.CommandContext(ctx, r.Python, "-m", "executor", "run", "--job", jobPath, "--out", outPath)
	cmd.Dir = r.ProjectRoot
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "ACCOUNTPOOL_LOCK_FD" || key == "ACCOUNTPOOL_RPC_FD" || strings.HasPrefix(key, "MAIL_") || strings.HasPrefix(key, "ACCOUNTPOOL_API_") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "ACCOUNTPOOL_LOCK_FD=3", "ACCOUNTPOOL_TRACE_FILE="+r.TraceFile)
	cmd.ExtraFiles = []*os.File{lock}
	if len(spec.Steps) == 1 && spec.Steps[0] == "register" {
		mail := r.Mail
		if mail == nil {
			mail = claude.NewMailClient()
		}
		socket, stop, err := mailBridge(ctx, mail, spec.MailJWT)
		if err != nil {
			return nil, err
		}
		defer stop()
		cmd.ExtraFiles = append(cmd.ExtraFiles, socket)
		cmd.Env = append(cmd.Env, "ACCOUNTPOOL_RPC_FD=4")
	}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Start()
	if runErr == nil {
		started = true
		if err := recordExecution(r.ProjectRoot, cmd.Process.Pid); err != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			cmd.Wait()
			return nil, err
		}
		runErr = cmd.Wait()
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err := os.WriteFile(filepath.Join(r.ProjectRoot, "artifacts/jobs/.last-finished"), []byte(time.Now().Format(time.RFC3339Nano)), 0600); err != nil {
			return nil, err
		}
		if err := os.Remove(journalPath(r.ProjectRoot)); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("executor produced no result (exit=%v); inspect executor.log", runErr)
	}
	if err := os.Chmod(outPath, 0600); err != nil {
		return nil, err
	}
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("invalid result JSON")
	}
	if res.Platform != spec.Platform || res.Account != spec.Account {
		return nil, fmt.Errorf("result identity does not match job")
	}
	switch res.Status {
	case "ok", "failed", "blocked":
	default:
		return nil, fmt.Errorf("invalid result status")
	}
	if runErr != nil && res.Status == "ok" {
		return nil, fmt.Errorf("executor exited unsuccessfully but reported ok")
	}
	return &res, nil
}
