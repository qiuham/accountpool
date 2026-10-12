-- 号池编排器 · 数据库 schema（SQLite）
--
-- 职责边界（DESIGN §0）
--   服务端持有：账号生命周期、调度、冷却、代理分配、墓碑、成本
--   执行侧持有：只跑任务，无状态，不碰这张库
--
-- 真相源约定
--   运行时状态（额度、限流）以 sub2api 为准；
--   本库只存 sub2api 装不下的：生命周期阶段、养号记录、物料映射、墓碑。

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;


-- ─────────────────────────────────────────────────────────────
-- 账号。号池的核心表。
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS accounts (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT    NOT NULL UNIQUE,      -- claude001
    email          TEXT,                         -- claude001@qiuhanming.com
    bundle_path    TEXT,                         -- bundles/claude001（执行侧的 profile 目录）

    -- 生命周期。状态机取值见 internal/pool/state.go
    state          TEXT    NOT NULL DEFAULT 'pending',

    -- 邮箱的收信 JWT。**只有建地址时返回一次**，列表接口不给，
    -- 所以必须持久化——否则"邮箱建了却永远收不了信"。
    mail_jwt       TEXT,

    -- 身份
    org_id         TEXT,                         -- lastActiveOrg，充值/关联都用它

    -- 会话快照。**这是短命的派生品**（实测几小时失效），
    -- profile 才是活的会话资产。这里存一份只为发 HTTP 检测时省事，
    -- 丢了无所谓——从 profile 现摘即可，所以不参与任何关键判定。
    session_key    TEXT,

    -- 订阅（来自 analytics_subscription_plan，纯 HTTP 可读）
    plan           TEXT,                         -- claude_free / claude_max 5x
    rate_limit_tier TEXT,                        -- default_claude_ai / default_claude_max_5x

    -- OAuth 凭据。**订阅成功后才可能拿到**（免费号在 authorize 就被 403）。
    -- 这四个字段就是写 cca .credentials.json 所需要的全部（实测确认）。
    access_token   TEXT,
    refresh_token  TEXT,
    expires_at     INTEGER,                      -- 毫秒
    scopes         TEXT,                         -- JSON 数组字符串

    -- 时间线
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL,
    registered_at  TEXT,
    subscribed_at  TEXT,
    banned_at      TEXT,
    retired_at     TEXT,

    -- 养号与巡检
    warmup_count   INTEGER NOT NULL DEFAULT 0,
    last_warmup_at TEXT,
    last_check_at  TEXT,
    last_error     TEXT,

    note           TEXT
);

CREATE INDEX IF NOT EXISTS idx_accounts_state ON accounts(state);
CREATE INDEX IF NOT EXISTS idx_accounts_email ON accounts(email);


-- ─────────────────────────────────────────────────────────────
-- 事件流。**生命周期记录靠它，不靠 accounts 表上的时间戳。**
-- 有了它才能回答"这个号什么时候从 A 变成 B、因为什么"。
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    at         TEXT NOT NULL,
    kind       TEXT NOT NULL,      -- register/onboard/warmup/check/oauth/ban/retire/state
    from_state TEXT,
    to_state   TEXT,
    detail     TEXT                -- JSON，放原始证据（响应片段、错误码等）
);

CREATE INDEX IF NOT EXISTS idx_events_account ON events(account_id, at DESC);


-- ─────────────────────────────────────────────────────────────
-- 出口代理。阶段 4 才真正用，但表先建好——因为"同一个出口同时只跑一个注册"
-- 这条硬约束（DESIGN §4）要求调度器必须知道出口，不能后补。
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS proxies (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    url            TEXT    NOT NULL UNIQUE,
    kind           TEXT,                          -- datacenter / residential / mobile
    geo            TEXT,                          -- US / JP …（必须与时区、语言自洽）
    asn            TEXT,
    state          TEXT    NOT NULL DEFAULT 'available',  -- available/in_use/cooling/burned
    cooldown_until TEXT,
    last_used_at   TEXT,
    fail_count     INTEGER NOT NULL DEFAULT 0,
    sub2api_proxy_id INTEGER                      -- 同步进 sub2api 后的 id
);

CREATE INDEX IF NOT EXISTS idx_proxies_state ON proxies(state);


-- ─────────────────────────────────────────────────────────────
-- 墓碑。**防自杀循环的唯一手段**（DESIGN §6）。
-- 不加的话下一轮补号会重新分配到刚死掉的 IP / 指纹 / 域名，
-- 新号上来就死，而且会被误判成"注册器坏了"。
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS tombstones (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    kind   TEXT NOT NULL,     -- email / email_domain / fingerprint / proxy / subnet / asn
    value  TEXT NOT NULL,
    reason TEXT,
    at     TEXT NOT NULL,
    UNIQUE(kind, value)
);

CREATE INDEX IF NOT EXISTS idx_tombstones_kind ON tombstones(kind, value);


-- ─────────────────────────────────────────────────────────────
-- 任务队列。服务端派活给执行侧，一条记录一次调用。
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id   INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    kind         TEXT    NOT NULL,               -- register/onboard/warmup/check/oauth
    state        TEXT    NOT NULL DEFAULT 'pending',  -- pending/running/ok/failed/blocked
    proxy_id     INTEGER REFERENCES proxies(id), -- 这次用哪个出口（调度决定的）

    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 2,

    -- 执行侧回传的机器可判原因。
    -- failed 值得原样重试（如邮件没到）；blocked 重试无用（如 CF 过不去、被验证拦住）。
    reason       TEXT,
    detail       TEXT,

    created_at   TEXT NOT NULL,
    started_at   TEXT,
    finished_at  TEXT,
    runner_out   TEXT                            -- result.json 路径
);

CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state, created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_account ON jobs(account_id, created_at DESC);

-- Worker control survives process restarts. until_unix=0 requires explicit resume.
CREATE TABLE IF NOT EXISTS scheduler_control (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    reason TEXT NOT NULL,
    until_unix INTEGER NOT NULL
);

-- Account-level binding survives individual jobs and is shared by CLI and export.
CREATE TABLE IF NOT EXISTS account_proxies (
    account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    proxy_id INTEGER NOT NULL REFERENCES proxies(id)
);

CREATE TABLE IF NOT EXISTS runtime_settings (
 id INTEGER PRIMARY KEY CHECK(id=1),
 value TEXT NOT NULL
);
INSERT OR IGNORE INTO runtime_settings(id,value) VALUES
(1,'{"gapSeconds":60,"cooldownSeconds":1800,"failureLimit":3,"taskTimeoutSeconds":1800}');

CREATE TABLE IF NOT EXISTS console_admin (
 id INTEGER PRIMARY KEY CHECK(id=1),
 username TEXT NOT NULL,
 salt TEXT NOT NULL,
 password_hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS console_sessions (
 token_hash TEXT PRIMARY KEY,
 expires INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS supply_settings(id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL);
INSERT OR IGNORE INTO supply_settings VALUES(1,'{"target":0,"daily":3,"checkHours":1,"sequenceDigits":3,"prefix":"free-","mailBase":"","registrationDomain":"","domains":[],"mailPassword":""}');

CREATE TABLE IF NOT EXISTS activity_settings(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);
INSERT OR IGNORE INTO activity_settings VALUES(1,'{"enabled":false,"intervalHours":24,"prompts":["你好，请用一句话分享一个日常生活小建议。"]}');
CREATE TABLE IF NOT EXISTS account_retries(
 account_id INTEGER PRIMARY KEY REFERENCES accounts(id),
 step TEXT NOT NULL,
 due_at INTEGER NOT NULL,
 failures INTEGER NOT NULL,
 reason TEXT NOT NULL
);

-- Hide cleared history from console without losing scheduler evidence.
CREATE TABLE IF NOT EXISTS cleared_job_history(job_id INTEGER PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE);

CREATE TABLE IF NOT EXISTS account_maintenance_pauses(
 account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 paused_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS mailbox_sequence(id INTEGER PRIMARY KEY CHECK(id=1), last_value INTEGER NOT NULL);
INSERT OR IGNORE INTO mailbox_sequence VALUES(1,0);

CREATE TABLE IF NOT EXISTS job_http_traces(job_id INTEGER PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE, captured_at INTEGER NOT NULL, payload TEXT NOT NULL);


CREATE TABLE IF NOT EXISTS executor_heartbeat(id INTEGER PRIMARY KEY CHECK(id=1),updated_at INTEGER NOT NULL);
