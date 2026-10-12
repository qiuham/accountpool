package store

import (
	"database/sql"
	"fmt"
	"time"
)

// 墓碑。**防自杀循环的唯一手段**（DESIGN §6）。
//
// 不加墓碑会陷入这个循环：下一轮补号重新分配到刚死掉的 IP / 指纹 / 域名，
// 新号上来就死——而且你会误判成"注册器坏了"，去改一个本来没坏的注册器。
//
// 三类东西必须进墓碑：
//   email / email_domain  被封号用过的地址和域名，永不重用
//   fingerprint           一个指纹只服务一个号，用完即焚
//   proxy / subnet / asn  死得多的出口段整体拉黑（比逐个 IP 拉黑有效）

// AddTombstone 记一条墓碑。重复记不报错（UNIQUE 冲突时更新原因）。
func (s *Store) AddTombstone(kind, value, reason string) error {
	if kind == "" || value == "" {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO tombstones (kind, value, reason, at) VALUES (?,?,?,?)
		ON CONFLICT(kind, value) DO UPDATE SET reason = excluded.reason, at = excluded.at`,
		kind, value, reason, time.Now().Format("2006-01-02 15:04:05"))
	return err
}

// IsTombstoned 查某个值是否在墓碑里。
func (s *Store) IsTombstoned(kind, value string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM tombstones WHERE kind = ? AND value = ?`,
		kind, value).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Tombstone 是墓碑记录。
type Tombstone struct {
	Kind   string
	Value  string
	Reason string
	At     string
}

// ListTombstones 列墓碑。kind 为空表示全部。
func (s *Store) ListTombstones(kind string, limit int) ([]Tombstone, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT kind, value, COALESCE(reason,''), at FROM tombstones`
	args := []any{}
	if kind != "" {
		q += " WHERE kind = ?"
		args = append(args, kind)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tombstone
	for rows.Next() {
		var t Tombstone
		if err := rows.Scan(&t.Kind, &t.Value, &t.Reason, &t.At); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ─────────────────────────── proxies ───────────────────────────
//
// 表先建着，因为 DESIGN §4 那条硬约束——「同一个出口同时只允许 1 个注册在跑」——
// 要求调度器必须知道出口。出口模型不先立起来，调度器没法写。

// Proxy 是一个出口。
type Proxy struct {
	ID            int64
	URL           string
	Kind          string // datacenter / residential / mobile
	Geo           string // US / JP …（必须与账号时区、语言自洽）
	ASN           string
	State         string // available / in_use / cooling / burned
	CooldownUntil string
	LastUsedAt    string
	FailCount     int
	Sub2APIID     sql.NullInt64
}

// AcquireProxy 原子地拿一个可用出口并标为 in_use。
//
// **这是"同一出口同时只跑一个注册"的落点**——并发单位是出口而不是任务
// （DESIGN §4：同 IP 并发注册是最强关联特征，比指纹还强）。
func (s *Store) AcquireProxy(geo string, id sql.NullInt64) (*Proxy, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().Format("2006-01-02 15:04:05")
	q := `SELECT id, url, COALESCE(kind,''), COALESCE(geo,''), COALESCE(asn,''),
	             state, COALESCE(fail_count,0)
	      FROM proxies
	      WHERE state = 'available'
	        AND (cooldown_until IS NULL OR cooldown_until < ?)`
	args := []any{now}
	if id.Valid {
		q += " AND id = ?"
		args = append(args, id.Int64)
	}
	if geo != "" {
		q += " AND geo = ?"
		args = append(args, geo)
	}
	q += " ORDER BY fail_count ASC, last_used_at ASC NULLS FIRST LIMIT 1"

	var p Proxy
	err = tx.QueryRow(q, args...).Scan(&p.ID, &p.URL, &p.Kind, &p.Geo, &p.ASN,
		&p.State, &p.FailCount)
	if err == sql.ErrNoRows {
		return nil, nil // 没有可用出口——这是个信号，不该静默继续
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(
		`UPDATE proxies SET state='in_use', last_used_at=? WHERE id=? AND state='available'`,
		now, p.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil // 被抢走
	}
	p.State = "in_use"
	return &p, tx.Commit()
}

// ReleaseProxy 释放出口。ok 为 false 时进冷却并累计失败次数。
//
// **失败要分级**（DESIGN §4）：网络超时、代理 5xx 不算风险信号，重试即可；
// 只有风控页、验证码这类才是。把两者混在一起，会出现"代理抖动导致全局降速"的假故障。
func (s *Store) ReleaseProxy(id int64, ok bool, cooldown time.Duration) error {
	if ok {
		_, err := s.db.Exec(
			`UPDATE proxies SET state='available', cooldown_until=NULL, fail_count=0 WHERE id=?`, id)
		return err
	}
	until := time.Now().Add(cooldown).Format("2006-01-02 15:04:05")
	_, err := s.db.Exec(`
		UPDATE proxies SET state='available', cooldown_until=?, fail_count=fail_count+1
		WHERE id=?`, until, id)
	return err
}

// AddProxy 登记一个出口。
func (s *Store) AddProxy(p Proxy) (int64, error) {
	if p.State == "" {
		p.State = "available"
	}
	res, err := s.db.Exec(`
		INSERT INTO proxies (url, kind, geo, asn, state) VALUES (?,?,?,?,?)`,
		p.URL, p.Kind, p.Geo, p.ASN, p.State)
	if err != nil {
		return 0, fmt.Errorf("登记出口: %w", err)
	}
	return res.LastInsertId()
}

// ListProxies 列出出口。
func (s *Store) ListProxies() ([]Proxy, error) {
	rows, err := s.db.Query(`
		SELECT id, url, COALESCE(kind,''), COALESCE(geo,''), COALESCE(asn,''),
		       state, COALESCE(cooldown_until,''), COALESCE(last_used_at,''),
		       COALESCE(fail_count,0)
		FROM proxies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proxy
	for rows.Next() {
		var p Proxy
		if err := rows.Scan(&p.ID, &p.URL, &p.Kind, &p.Geo, &p.ASN, &p.State,
			&p.CooldownUntil, &p.LastUsedAt, &p.FailCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ─────────────────────────── jobs 补充 ───────────────────────────

// GetAccountByID 按 id 取账号。
func (s *Store) GetAccountByID(id int64) (*Account, error) {
	row := s.db.QueryRow(`
		SELECT id, name, COALESCE(email,''), COALESCE(bundle_path,''), state,
		       COALESCE(mail_jwt,''), COALESCE(org_id,''), COALESCE(session_key,''),
		       COALESCE(plan,''), COALESCE(rate_limit_tier,''),
		       COALESCE(access_token,''), COALESCE(refresh_token,''),
		       COALESCE(expires_at,0), COALESCE(scopes,''),
		       created_at, updated_at, COALESCE(registered_at,''),
		       COALESCE(subscribed_at,''), COALESCE(banned_at,''), COALESCE(retired_at,''),
		       warmup_count, COALESCE(last_warmup_at,''), COALESCE(last_check_at,''),
		       COALESCE(last_error,''), COALESCE(note,'')
		FROM accounts WHERE id = ?`, id)
	var a Account
	err := row.Scan(&a.ID, &a.Name, &a.Email, &a.BundlePath, &a.State, &a.MailJWT,
		&a.OrgID, &a.SessionKey, &a.Plan, &a.RateLimitTier, &a.AccessToken,
		&a.RefreshToken, &a.ExpiresAt, &a.Scopes, &a.CreatedAt, &a.UpdatedAt,
		&a.RegisteredAt, &a.SubscribedAt, &a.BannedAt, &a.RetiredAt, &a.WarmupCount,
		&a.LastWarmupAt, &a.LastCheckAt, &a.LastError, &a.Note)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// RequeueJob 保留出口绑定，重试不会换用其他出口。
func (s *Store) RequeueJob(id int64, attempted bool) error {
	decrement := 0
	if !attempted {
		decrement = 1
	}
	_, err := s.db.Exec(
		`UPDATE jobs SET state='pending', started_at=NULL, attempts=MAX(0,attempts-?) WHERE id=?`, decrement, id)
	return err
}

// PendingJobs 数待跑任务，给调度器判断"还有没有活"。
func (s *Store) PendingJobs() (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM jobs WHERE state='pending'`).Scan(&n)
	return n, err
}

// AccountHistory 取账号最近的状态变更（不含噪音事件）。
func (s *Store) AccountHistory(id int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT id, account_id, at, kind, COALESCE(from_state,''), COALESCE(to_state,''),
		       COALESCE(detail,'')
		FROM events WHERE account_id = ? AND from_state != to_state
		ORDER BY id DESC LIMIT ?`, id, limit)
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

// ListJobs 列最近的任务。
func (s *Store) ListJobs(limit int) ([]*Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, account_id, kind, state, proxy_id, attempts,
		       max_attempts, COALESCE(reason,''), COALESCE(detail,''), created_at,
		       COALESCE(started_at,''), COALESCE(finished_at,''), COALESCE(runner_out,'')
		FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.AccountID, &j.Kind, &j.State, &j.ProxyID,
			&j.Attempts, &j.MaxAttempt, &j.Reason, &j.Detail, &j.CreatedAt,
			&j.StartedAt, &j.FinishedAt, &j.RunnerOut); err != nil {
			return nil, err
		}
		out = append(out, &j)
	}
	return out, rows.Err()
}

// UnlockProxy releases an unused lease without changing observed failure history.
func (s *Store) UnlockProxy(id int64) error {
	_, err := s.db.Exec("UPDATE proxies SET state='available' WHERE id=?", id)
	return err
}
func (s *Store) BindJobProxy(jobID, proxyID int64) error {
	_, err := s.db.Exec("UPDATE jobs SET proxy_id=? WHERE id=?", proxyID, jobID)
	return err
}
