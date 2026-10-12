// Package store 负责持久化。SQLite + 纯 Go 驱动（modernc.org/sqlite），
// 不需要 CGO，编译出来是静态二进制，部署就是拷一个文件。
//
// 为什么是 SQLite 而不是 Redis/PG（对比同作者的另两个项目）：
//
//	CRS 用 Redis     —— 它是中转服务，存 API key 和限流计数，要的是速度，数据丢了可重建
//	sub2api 用 PG    —— 它是号池网关，要分页/筛选/统计用量，关系型才撑得住
//	我们用 SQLite    —— 要查询（"列出所有 ok 且已订阅的号"）所以排除 Redis；
//	                    单机、数据量小、要零运维所以不必上 PG
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

// Account 是号池的核心实体。字段划分见 schema.sql 的注释。
type Account struct {
	ID         int64
	Name       string
	Email      string // Platform login identity.
	BundlePath string
	State      string
	MailJWT    string
	OrgID      string

	// 会话快照：短命的派生品，丢了从 profile 现摘。不参与关键判定。
	SessionKey string

	// 订阅（纯 HTTP 可读）
	Plan          string
	RateLimitTier string

	// OAuth 凭据：订阅成功后才可能拿到（免费号在 authorize 就被 403）。
	// 这四个字段就是写 cca .credentials.json 所需的全部。
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
	Scopes       string

	CreatedAt    string
	UpdatedAt    string
	RegisteredAt string
	SubscribedAt string
	BannedAt     string
	RetiredAt    string

	WarmupCount  int
	LastWarmupAt string
	LastCheckAt  string
	LastError    string
	Note         string
}

// Job 是一次派给执行侧的任务。
type Job struct {
	ID         int64
	AccountID  int64
	Kind       string // register/onboard/warmup/check/oauth
	State      string // pending/running/ok/failed/blocked
	ProxyID    sql.NullInt64
	Attempts   int
	MaxAttempt int
	Reason     string
	Detail     string
	CreatedAt  string
	StartedAt  string
	FinishedAt string
	RunnerOut  string
}

// Event 是生命周期记录。**状态变更靠它追溯，不靠 accounts 表上的时间戳。**
type Event struct {
	ID        int64
	AccountID int64
	At        string
	Kind      string
	FromState string
	ToState   string
	Detail    string
}

func now() string { return time.Now().Format("2006-01-02 15:04:05") }

// Store 包住 *sql.DB。用类型而不是裸 db，是为了把"只能通过方法改状态"这件事
// 变得自然——状态变更必须记事件，裸 db 容易绕过。
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

// DB 暴露原始连接，给需要自定义查询的地方用。改动数据请优先用上面的方法。
func (s *Store) DB() *sql.DB { return s.db }

// Open 打开库并应用 schema。schema 用 IF NOT EXISTS，重复执行安全。
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	// SQLite 写是串行的，连接数开太多只会互相等锁
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	buf, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return nil, fmt.Errorf("读 schema: %w", err)
	}
	if _, err := db.Exec(string(buf)); err != nil {
		return nil, fmt.Errorf("应用 schema: %w", err)
	}
	return db, nil
}

// ─────────────────────────── accounts ───────────────────────────

// CreateAccount 建号并记一条事件。两者在同一事务里，避免"号建了但没历史"。
func (s *Store) CreateAccount(a *Account) (int64, error) {
	t := now()
	a.CreatedAt, a.UpdatedAt = t, t
	if a.State == "" {
		a.State = "pending"
	}
	res, err := s.db.Exec(`
		INSERT INTO accounts (name, email, bundle_path, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		a.Name, a.Email, a.BundlePath, a.State, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return 0, fmt.Errorf("建号: %w", err)
	}
	id, _ := res.LastInsertId()
	if err := s.AddEvent(id, "create", "", a.State, ""); err != nil {
		return id, err
	}
	return id, nil
}

// UpdateState 改状态并记事件。**所有状态变更都必须走这里**，
// 否则事件流会跟实际状态对不上，追溯就失去意义。
func (s *Store) UpdateState(id int64, to, kind, detail string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var from string
	if err := tx.QueryRow(`SELECT state FROM accounts WHERE id = ?`, id).Scan(&from); err != nil {
		return fmt.Errorf("读当前状态: %w", err)
	}
	if from == to {
		return nil // 状态没变就不记，避免事件流被噪音灌满
	}
	if _, err := tx.Exec(
		`UPDATE accounts SET state = ?, updated_at = ? WHERE id = ?`, to, now(), id); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO events (account_id, at, kind, from_state, to_state, detail) VALUES (?,?,?,?,?,?)`,
		id, now(), kind, from, to, detail); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateFields 更新非状态字段。用 map 只写要改的列，避免全字段覆盖。
func (s *Store) UpdateFields(id int64, fields map[string]any) error {
	return updateFields(s.db, id, fields)
}

type fieldExecutor interface {
	Exec(string, ...any) (sql.Result, error)
}

func updateFields(db fieldExecutor, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	allowed := map[string]bool{
		"email": true, "bundle_path": true, "mail_jwt": true,
		"org_id": true, "session_key": true,
		"plan": true, "rate_limit_tier": true, "access_token": true,
		"refresh_token": true, "expires_at": true, "scopes": true,
		"registered_at": true, "subscribed_at": true, "banned_at": true,
		"retired_at": true, "warmup_count": true, "last_warmup_at": true,
		"last_check_at": true, "last_error": true, "note": true,
	}
	cols, args := "", []any{}
	for k, v := range fields {
		if !allowed[k] {
			return fmt.Errorf("字段 %q 不允许更新", k)
		}
		if cols != "" {
			cols += ", "
		}
		cols += k + " = ?"
		args = append(args, v)
	}
	args = append(args, now(), id)
	if _, err := db.Exec(
		`UPDATE accounts SET `+cols+`, updated_at = ? WHERE id = ?`, args...); err != nil {
		return err
	}
	return nil
}

// GetAccount 按名字取。
func (s *Store) GetAccount(name string) (*Account, error) {
	row := s.db.QueryRow(`
		SELECT id, name, COALESCE(email,''), COALESCE(bundle_path,''), state,
		       COALESCE(mail_jwt,''), COALESCE(org_id,''), COALESCE(session_key,''), COALESCE(plan,''),
		       COALESCE(rate_limit_tier,''), COALESCE(access_token,''),
		       COALESCE(refresh_token,''), COALESCE(expires_at,0), COALESCE(scopes,''),
		       created_at, updated_at, COALESCE(registered_at,''),
		       COALESCE(subscribed_at,''), COALESCE(banned_at,''), COALESCE(retired_at,''),
		       warmup_count, COALESCE(last_warmup_at,''), COALESCE(last_check_at,''),
		       COALESCE(last_error,''), COALESCE(note,'')
		FROM accounts WHERE name = ?`, name)
	var a Account
	err := row.Scan(&a.ID, &a.Name, &a.Email, &a.BundlePath, &a.State, &a.MailJWT, &a.OrgID,
		&a.SessionKey, &a.Plan, &a.RateLimitTier, &a.AccessToken, &a.RefreshToken,
		&a.ExpiresAt, &a.Scopes, &a.CreatedAt, &a.UpdatedAt, &a.RegisteredAt,
		&a.SubscribedAt, &a.BannedAt, &a.RetiredAt, &a.WarmupCount,
		&a.LastWarmupAt, &a.LastCheckAt, &a.LastError, &a.Note)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListAccounts 按状态筛选。states 为空表示全部。
func (s *Store) ListAccounts(states ...string) ([]*Account, error) {
	q := `SELECT name, state, COALESCE(email,''), COALESCE(plan,''), warmup_count,
	             COALESCE(last_warmup_at,''), COALESCE(last_error,'')
	      FROM accounts`
	args := []any{}
	if len(states) > 0 {
		q += " WHERE state IN (" + placeholders(len(states)) + ")"
		for _, st := range states {
			args = append(args, st)
		}
	}
	q += " ORDER BY id"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.Name, &a.State, &a.Email, &a.Plan,
			&a.WarmupCount, &a.LastWarmupAt, &a.LastError); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// ─────────────────────────── events ───────────────────────────

func (s *Store) AddEvent(accountID int64, kind, from, to, detail string) error {
	_, err := s.db.Exec(
		`INSERT INTO events (account_id, at, kind, from_state, to_state, detail)
		 VALUES (?,?,?,?,?,?)`, accountID, now(), kind, from, to, detail)
	return err
}

// Events 取某个账号的时间线（倒序）。
func (s *Store) Events(accountID int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, account_id, at, kind, COALESCE(from_state,''), COALESCE(to_state,''),
		       COALESCE(detail,'')
		FROM events WHERE account_id = ? ORDER BY id DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.AccountID, &e.At, &e.Kind,
			&e.FromState, &e.ToState, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ─────────────────────────── jobs ───────────────────────────

func (s *Store) CreateJob(accountID int64, kind string, proxyID sql.NullInt64) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO jobs (account_id, kind, state, proxy_id, created_at)
		VALUES (?, ?, 'pending', ?, ?)`, accountID, kind, proxyID, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ClaimJob 原子地取一个待跑任务并标为 running。
// 用 UPDATE ... WHERE state='pending' 的返回行数做乐观锁，避免两个调度器拿到同一条。
func (s *Store) ClaimJob() (*Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// The current database is a single Claude platform pool. All registration
	// attempts (including re-login) share this persisted platform-wide gate.
	var gap int
	if err := tx.QueryRow("SELECT COALESCE(json_extract(value,'$.registrationGapMinutes'),0) FROM supply_settings WHERE id=1").Scan(&gap); err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-time.Duration(gap) * time.Minute).Format("2006-01-02 15:04:05")

	var j Job
	err = tx.QueryRow(`
		SELECT id, account_id, kind, proxy_id, attempts, max_attempts
		FROM jobs WHERE state = 'pending' AND account_id NOT IN (SELECT account_id FROM account_maintenance_pauses)
        AND (instr(','||kind||',', ',register,')=0 OR NOT EXISTS (
            SELECT 1 FROM jobs previous
            WHERE instr(','||previous.kind||',', ',register,')>0
            AND previous.started_at IS NOT NULL
            AND COALESCE(previous.reason,'') NOT IN ('mailbox-create-failed','mailbox-prerequisite-failed')
            AND (previous.state='running' OR COALESCE(previous.finished_at,previous.started_at)>?)
        )) ORDER BY id LIMIT 1`, cutoff).
		Scan(&j.ID, &j.AccountID, &j.Kind, &j.ProxyID, &j.Attempts, &j.MaxAttempt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(
		`UPDATE jobs SET state='running', started_at=?, attempts=attempts+1
		 WHERE id=? AND state='pending'`, now(), j.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil // 被别人抢走了
	}
	j.State, j.Attempts = "running", j.Attempts+1
	return &j, tx.Commit()
}

// FinishJob 收尾。reason 是执行侧回传的机器可判原因。
func (s *Store) FinishJob(id int64, state, reason, detail, out string) error {
	_, err := s.db.Exec(`
		UPDATE jobs SET state=?, reason=?, detail=?, runner_out=?, finished_at=?
		WHERE id=?`, state, reason, detail, out, now(), id)
	return err
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}
