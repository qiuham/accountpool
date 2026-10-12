# AccountPool

Go 编排和 CRS 导出服务，Python/Camoufox 执行浏览器任务。下游账号调度及 OAuth 刷新由 sub2api 负责。

## 安装与启动

要求：Go（版本见 `backend/go.mod`）、Python 及 Camoufox 所需系统依赖；可见浏览器需要可用桌面会话。

```sh
python3 -m venv .venv-stable
.venv-stable/bin/pip install -r executor/requirements.txt
.venv-stable/bin/python -m camoufox fetch
(cd backend && go build -o ../bin/accountpool ./cmd/accountpool)
cp config.example.toml config.toml  # 仅首次安装
cp .env.example .env  # 仅首次安装；不要覆盖已有凭据
chmod 600 .env config.toml
```

编辑 `.env` 中的邮箱和接口凭据。`MAIL_ADMIN_AUTH` 是邮箱管理员凭据；`ACCOUNTPOOL_API_USER/PASSWORD` 是本服务供 sub2api 拉取时使用的凭据，并非 Claude 登录密码。`config.toml` 配置浏览器、时间参数及路径。

新建邮箱配置统一放在 `.env`：

```dotenv
MAIL_BASE=https://mail.example.com
MAIL_PREFIX=pool-
MAIL_DOMAIN=example.com
MAIL_ADMIN_AUTH=填写邮箱服务管理员密码
```

自动邮箱申请形如 `pool-0123456789abcdef@example.com`（内部账号名与邮箱 local part 相同）。`MAIL_PREFIX` 未设置时为 `free-`，显式留空则只保留随机部分；允许 ASCII 字母、数字、`-`、`_`，最多 48 字符。域名和管理员鉴权值直接读取配置，未硬编码。最终使用邮箱服务返回的完整地址。

修改配置后执行 `docker compose up -d --force-recreate` 让容器读取新环境。已有邮箱/账号名保持不变；改前缀不会重置每日新建额度。管理员密码不要写入 CLI 参数或提交仓库。手动 `add` 的账号名不追加自动前缀。

统一从 `scripts/accountpool` 启动，它加载本地 `.env` 并设置项目根目录。直接运行 `bin/accountpool` 时需自行设置环境变量。

```sh
scripts/accountpool list
scripts/accountpool add ACCOUNT --email ADDRESS --proxy http://HOST:PORT
scripts/accountpool run ACCOUNT --steps register,onboard
scripts/accountpool run ACCOUNT --steps check,oauth
scripts/accountpool job add ACCOUNT check
scripts/accountpool worker
scripts/accountpool serve
```

`add --proxy` 将出口绑定到账号。worker 首次分配的出口也会持久绑定，后续任务不得自动替换。未绑定的旧账号不会被自动推断或更改出口。

## 执行与恢复

上游请求串行执行，共享执行锁和至少 60 秒的任务间冷却。手动操作也记录持久任务；进程中断后的未知结果会暂停而不自动重放。worker 的退避和暂停在重启后保留；`run` 也检查持久暂停。明确检查失败原因后可用 `worker --resume` 解除暂停，该命令会继续处理待执行队列，但不会重放结果不明的中断任务。

Python 入口仅支持 `register/onboard/warmup/session`。注册必须由 Go 启动才能取得收信 IPC 通道；HTTP 检查和 OAuth 不再有 Python 入口。独立浏览器调试入口仍有执行锁和冷却，但不读取 Go 的暂停数据库。日常使用统一 Go 入口，手动任务和 worker 共享上游挑战暂停策略。进程组恢复覆盖 Go 启动并记录的执行器。

## sub2api 对接

服务默认仅监听 `127.0.0.1:8787`，提供 `/healthz`、`POST /web/auth/login`、`GET /admin/sync/export-accounts`。跨主机访问需要部署者提供受控网络和 TLS 入口。

在 sub2api 的 CRS 同步功能中填写服务地址及接口用户名/密码，开启 **SyncProxies**。同步触发及周期由下游部署负责，本服务不主动推送，也不保证下游安装后自动定时拉取。

- 仅导出有完整 OAuth 凭据的账号；网页登录成功不代表具有 OAuth 使用权限。
- `expires_at` 导出 Unix 秒，本地仍保存毫秒。
- 已停用/退役且保留 OAuth 凭据的账号继续导出，明确设置 `isActive=false`、`schedulable=false`。从列表消失不会自动删除下游账号。
- 停用信息最早在下一次成功同步时生效；紧急停用需同时操作下游。
- 不要在下游确认停用前删除本地账号或 OAuth 凭据，否则 CRS 客户端会跳过记录。
- 导出的代理地址必须能从 sub2api 主机访问；本地回环代理不适合直接跨主机同步。

## 部署

使用 Docker Compose，前端、API 和维护器各一个容器，共用 SQLite、浏览器 Profile 和执行锁卷；维护器只能运行一个副本。

```sh
cp .env.example .env          # 新部署时执行，已有文件不要覆盖
cp config.example.toml config.toml
# 将 accountpool-web 放在本仓库同级目录；编辑部署参数和浏览器代理
# 管理员初始化、邮箱与账号目标在网页配置
chmod 600 .env config.toml
# 非 root 容器 UID=1000，需要读取挂载的 config.toml
# 若宿主用户不是 UID=1000，使用 ACL 授予该 UID 只读权限
# setfacl -m u:1000:r config.toml
docker compose up -d --build
docker compose logs -f maintenance
```

容器内浏览器运行于 Xvfb；不会在宿主桌面弹窗。遇到人工挑战会保留诊断并暂停，不会无休止重试。代理指向宿主机时，使用 `http://host.docker.internal:PORT` 并让代理监听容器可达地址。默认发布本机 5188（网页）与 8787（接口）端口。sub2api 在同一 Docker 网络时可用 `http://api:8787`；跨主机需配置受控入口。

三个命名卷保存数据库、Profile 和产物；重建容器保留它们。不要使用 `docker compose down -v` 删除账号资料。新 Docker 部署默认是新库，不会自动接管宿主现有数据库/Profile。已有资料需停止所有执行器后离线恢复到相应卷，不能只复制数据库。

首次启动访问 http://localhost:5188，使用 API 日志中的初始化密钥创建管理员。管理员密码与 CRS 导出密码分离。详细步骤见 [管理台部署](docs/console-deployment.md)。

### 自动维护免费池

- 配置中心「免费账号目标」：免费注册账号目标，默认 0，不自动新建。降低目标不删除已有账号。
- 配置中心「检查周期」：健康检查周期，默认 1h，最短 1h。
- 配置中心「每日新增上限」：每天最多自动创建数量，默认 3，失败也占额度；按容器 UTC 日期累计，重启不清零。
- 维护器每分钟醒来，按 `register → onboard → check` 推进；可在配置中心启用定时网页消息，设置间隔与内容，每次仅发送一条。
- 免费可用数要求：明确 `claude_free`、已有会话、引导成功、最近健康检查成功且未过期。它表示注册/健康流程完成，不保证聊天额度或每次回复成功。
- 在途、等待检查的账号单独占预留名额，防止重启/反复巡检重复补号。未知健康不算可用，也不无限创建替代账号。
- 检查发现订阅后自动排除免费名额并执行一次 OAuth；有 OAuth 凭据的账号不参与免费维护，刷新交给 sub2api。免费池再补缺额。
- 会话过期自动重登；明确失败进入持久化冷却后重试，连续失败指数退避，最长 24 小时。
- 任务串行，间隔至少 60 秒。挑战/限流按冷却时间暂停；可重试失败到期自动恢复。未知提交结果与失败 OAuth 不自动重放。

排查后恢复，先停止维护器，避免与手动 worker 争抢执行锁：

```sh
docker compose stop maintenance
docker compose run --rm maintenance worker --resume
# 失败任务需根据诊断显式修复/重跑；resume 不重发失败任务
docker compose up -d maintenance
```

本机同一实现的入口为 `scripts/accountpool maintain`。`--once` 处理一轮直到队列无工作或暂停；不等于只执行一个任务。

## 测试及验收边界

```sh
(cd backend && go test ./... -count=1)
.venv-stable/bin/python -m unittest discover -s tests
```

本地回归覆盖接口契约、代理绑定、异常中断及暂停恢复。真实下游导入、OAuth 刷新及调用需在具备权限的账号和目标 sub2api 实例上单独验收；本地 fixture 通过不等于该链路已通过。不要通过反复注册或并发请求补充样本。

## 备份与恢复

先停止接口、worker 和浏览器。默认布局（`backend/pool.db`、`bundles/`）可用：

```sh
python3 scripts/backup.py backup "$PWD" /PRIVATE_PATH/snapshot
python3 scripts/backup.py restore /PRIVATE_PATH/snapshot /PRIVATE_PATH/restored-data
```

脚本使用 SQLite backup API、校验数据库完整性并记录文件校验和，包含 profile、配置、私有环境变量及冷却时间。恢复只写入不存在的新目录，不覆盖运行项目，也不自动启动服务。自定义 `ACCOUNTPOOL_DB` 或 bundle 路径不适用此脚本。备份包含登录凭据，目录保持私有；恢复部署时先完成软件安装，再离线放回数据。

## 当前职责与目录

```text
scripts/accountpool
  └─ Go CLI / worker / maintain
      ├─ internal/pool      免费池水位、状态、串行队列、暂停策略、统一结果提交
      ├─ internal/store     SQLite、凭据、事件、代理绑定
      ├─ internal/claude    OAuth/PKCE、健康与订阅查询、邮箱与登录凭证解析
      ├─ internal/lifecycle 退役、邮箱与 Profile 清理
      ├─ internal/api       CRS 登录与导出
      └─ internal/runner    原生 HTTP / 浏览器步骤编排、执行锁、进程恢复
           ├─ 原生 check / oauth → Claude HTTP
           └─ Python executor
               ├─ register / onboard / warmup → Camoufox
               ├─ session → 当前 Profile Cookie 读取
               └─ browser_services → 私有继承 socket → Go 邮箱服务
```

注册页打开期间，Python 通过私有 IPC 请求 Go 记录收信水位，提交一次表单，再请求新邮件凭证。Go 负责轮询、超时、错误分类及筛选，只返回本次登录链接或验证码。邮箱 JWT、管理员密码不进入浏览器进程、参数或任务 JSON。

Go 原生 HTTP 步骤先通过 Python 读取当前 Firefox 会话，再执行 HTTP；不会启动浏览器，也不会扫描历史 OAuth/check 产物。所有步骤结果按当前调用记录，凭据、状态、事件在一个 SQLite 事务内提交。OAuth 刷新仍由 sub2api 负责。

## 退役

```sh
scripts/accountpool retire ACCOUNT
# 等下游完成同步并确认账号已停用，再清理本地资源：
scripts/accountpool retire ACCOUNT --purge --downstream-disabled
```

第一步仅写入停用状态，保留资料。第二步删除邮箱和 Profile，保留账号及 OAuth 停用记录；邮箱删除失败时不删除 Profile。退役账号的旧队列任务会被阻止执行。此命令不会自动访问或操作 sub2api。
