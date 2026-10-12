# 历史设计说明

当前实现与运维以 README.md 为准。本文包含未实现的规划。CRS 现保留带 OAuth 凭据的停用/退役记录以停用下游；不再通过省略记录表示删除。

# Claude 号池供给与生命周期编排方案

> 基线：`Wei-Shaw/sub2api` 本地克隆 `/home/ham/sub2api-native-wire-poc-20260923/backend`，接口与字段均逐行核对自源码，非推测。
> 本文只做方案，不含实现代码。

---

## 0. 核心判断

**不要另造一个号池。sub2api 已经是号池。**

sub2api 现成具备：账号存储、出站代理池与账号绑定、令牌自动刷新、调度、并发与限流、临时不可调度、账号测试探测、用量与今日统计、批量操作、审计日志。

所以新系统的正确定位是 **供给控制器 + 生命周期编排器（Orchestrator）**，把 sub2api 当作下游号池 API 使用。它只管 sub2api 管不了的东西：

| 归属 | 职责 |
|---|---|
| **Orchestrator（新建）** | 注册物料池、身份一致性域、注册流水线、养号流水线、封号判定、墓碑台账、水位控制、成本账本 |
| **sub2api（已有）** | 账号存储、出站代理、令牌刷新、调度出站、限流、用量统计、探测 |

这条边界定死，能省掉一半以上的实现量：不要自己写调度器，不要自己写 token 刷新，不要自己写用量统计。

**真相源约定**：账号的运行时状态以 sub2api 为准；Orchestrator 本地库只存 sub2api 装不下的元数据（物料映射、生命周期阶段、养号记录、成本、墓碑）。不做双向同步，会打架。

---

## 1. 与 sub2api 的接口契约（已核对）

认证：`x-api-key: <admin-api-key>`（`internal/server/middleware/admin_auth.go:26`）。

**Orchestrator 会用到的端点**：

| 用途 | 端点 |
|---|---|
| sessionKey 换 OAuth 凭据 | `POST /api/v1/admin/accounts/cookie-auth` body `{code: <sessionKey>, proxy_id}` → `TokenInfo` |
| 建号 | `POST /api/v1/admin/accounts` |
| 批量建号 | `POST /api/v1/admin/accounts/batch` |
| 列表/对账 | `GET /api/v1/admin/accounts` |
| 测试探测 | `POST /api/v1/admin/accounts/:id/test` |
| 刷新令牌 | `POST /api/v1/admin/accounts/:id/refresh` |
| 刷新档位（判断订阅） | `POST /api/v1/admin/accounts/:id/refresh-tier` |
| 用量 | `GET /api/v1/admin/accounts/:id/usage` |
| 临时不可调度 | `GET` / `DELETE` / `POST .../temp-unschedulable` |
| 调度开关 | `POST /api/v1/admin/accounts/:id/schedulable` |
| 删号 | `DELETE /api/v1/admin/accounts/:id`、`POST /batch-delete` |
| 批量改 | `POST /bulk-update`、`POST /batch-update-credentials` |
| 代理池 | `/api/v1/admin/proxies`（账号通过 `proxy_id` 绑定） |
| CRS 同步 | `POST /api/v1/admin/accounts/sync/crs`、`POST .../sync/crs/preview` |

**凭据结构**（`internal/service/oauth_service.go:135-146`）：

```
TokenInfo { access_token, token_type, expires_in, expires_at,
            refresh_token, scope, org_uuid, account_uuid, email_address }
```

建号请求体关键字段（`internal/handler/admin/account_handler.go:121`）：
`name / platform / type(oauth|setup-token|apikey) / credentials / extra / proxy_id / concurrency / priority / load_factor / group_ids / expires_at`。

**两个必须利用的既有能力**：

1. **建号接口幂等**。`internal/handler/admin/account_handler.go:1038` 走 `executeAdminIdempotent`，重放时回 `X-Idempotency-Replayed: true`。所以同步器带稳定幂等键重放是安全的，不需要自己实现去重。
2. **`cookie-auth` 直接吃 sessionKey**。这意味着**注册器只需要产出 claude.ai 的 `sessionKey`**，OAuth 兑换由 sub2api 完成。注册器的职责因此收窄一大截——不要去自己实现 OAuth 授权码流程。

---

## 2. 同步通路：选 pull，不选 push

有两条路，工作量差一个量级。

**路子 A（push）**：Orchestrator 主动调 `cookie-auth` → `POST /accounts`。需要持有 sub2api 的 admin api key，需要自己维护幂等键与重试，需要处理"本地已建但远程失败"的中间态。

**路子 B（pull，推荐）**：Orchestrator 实现两个 **CRS 兼容端点**（登录 + 导出），sub2api 定期主动来拉。sub2api 侧现成客户端在 `internal/service/crs_sync_service.go`：

- 输入 `SyncFromCRSInput { BaseURL, Username, Password, SyncProxies, SelectedAccountIDs }`（`crs_sync_service.go:74`）
- sub2api 先 POST 登录拿 token，再拉导出，读取 `crsExportResponse.data.claudeAccounts[]`
- 匹配键是 `crs_account_id`，**按 ID upsert，天然幂等**（`crs_sync_service.go:50` 注释明确）
- 还支持 `SyncProxies`，代理可以一起带过去

`claudeAccounts[]` 每条需要提供（`crs_sync_service.go:127-145`）：

```
kind / id / name / description / platform / authType(oauth|setup-token)
isActive / schedulable / priority / status
proxy { protocol, host, port, username, password }
credentials { ...TokenInfo }
extra { ... }
```

选 B 的理由：Orchestrator 不需要知道 sub2api 的 admin key，不需要实现幂等与重试，不需要处理中间态，**sub2api 的 `crs_account_id` 匹配把同步复杂度全部吸收掉了**。Orchestrator 只要保证"导出的是当前应该存在的号的状态"。

注意点：
- CRS 是 pull 模型，所以**节奏由 sub2api 决定**。要在 sub2api 侧把同步周期调到自己能接受的粒度，同时 Orchestrator 的导出必须能反映"刚刚封掉/刚拉黑"的变化，否则 sub2api 会继续用死号。
- 导出接口必须只导出"应存在"的号。墓碑中的号不出现在导出里；已被拉黑的号要显式给 `isActive:false` + `schedulable:false`（比直接消失更安全，因为消失依赖 sub2api 的删除逻辑，显式置灰是确定行为）。
- **两条路可以并存**：pull 做常态同步，push 只用于"紧急止血"（发现封号要立刻让 sub2api 停调度）。止血走 `POST /:id/schedulable {schedulable:false}` 或 `bulk-update`，延迟最低。

---

## 3. 身份一致性域（Identity Bundle）

回答"特征隔离"。要点是：**不是分散的指纹，是绑定后不可拆的一致性域。**

一个 bundle 是一个原子单位：

```
Bundle {
  proxy        出口（住宅/移动，sticky session，geo, asn）
  timezone     与 proxy.geo 严格对应
  locale/lang  与 geo 对应
  fingerprint  UA / canvas / webgl / fonts / screen / hwConcurrency / 音频
  persona      行为档案（打字节奏、句式、话题偏好、活跃时段）
  email        地址 + 域名
  sub2api_proxy_id   在 sub2api 里对应的代理记录
}
```

三条不变量，任何一条破了整个号就白养：

1. **内部自洽**：`proxy.geo == timezone == locale == 活跃时段`。日本出口 + UTC 时区，一次请求就露。
2. **跨号零碰撞**：`coherence_hash` 加唯一索引；指纹一对一（一个指纹只服务一个号，用完进墓碑不许回收）。两个号撞同一指纹，等于在上游眼里自报关联。
3. **全阶段不变**：注册 → 养号 → 订阅 → 运行，**全程同一 bundle**。最容易漏的是"注册用住宅代理、养号换成机房 IP"——这是自曝。

**必须在 sub2api 侧落实的部分**：sub2api 出站时用的是账号绑定的 `proxy_id`。如果所有号在 sub2api 里共用默认出口，前面所有隔离在出口层被抹平。所以 bundle 的代理必须同步进 sub2api 代理池，并绑定到对应账号（走 CRS 导出的 `proxy` 字段 + `SyncProxies`，或 push 时的 `proxy_id`）。

> **前置风险，优先级高于本方案其余全部内容**：你自己的审计 `sub2api-audit-20260922/AUDIT-未透传特征清单.md` 已证实 sub2api 注入固定身份——工具名 `python__sub2api`、确定性 UUID、`sub2api:codex-install-id:v2:` 盐值会话标识、固定 UA/身份块。**这些特征会把所有走 sub2api 的账号在上游聚成一坨。** 号池隔离做得再干净，这一层不收口就是零收益。建议把 sub2api 的指纹收敛放在规模化之前做。

---

## 4. 注册并发、冷却与自适应限速

回答"要不要冷却、能不能并发"。

**能并发，但并发的单位不是任务，是出口。**

三层限速，维度独立：

1. **出口维度（硬约束）**：同一个出口（住宅 IP / sticky 会话）同一时刻只允许 1 个注册在跑，完成后进冷却。这是物理红线——同 IP 并发注册是最强关联特征，比指纹强。
2. **域维度**：同一邮箱域名单位时间新号数上限 + 发信量上限。目的不是骗过注册接口，而是**防止自己的域名被标记**（域名被标 = 整个域名池报废）。
3. **全局维度**：自适应并发控制器。

自适应控制器（AIMD）：

```
初始并发 C = 1
每窗口（N 次尝试，如 20 次）结算：
  成功率 >= 0.85 且窗口内无风控信号  →  C = min(C + 1, C_max)
  出现风控信号（风控页/验证码/异常跳转）→  C = max(1, C // 2)，进全局冷却
```

原则：**没有正面信号就不加速**。默认从 1 起，靠成功累积往上走，不靠配置拍脑袋。

**冷却分级**（不能只有一个冷却值）：

| 层级 | 触发 | 策略 |
|---|---|---|
| 单出口冷却 | 该出口一次软失败 | 30 min 起，指数退避，上限 24 h |
| 单任务重试 | 注册 job 失败 | 最多 2 次，**必须换出口**，禁止同出口重试 |
| 全局熔断 | 窗口内失败率超阈值 | **全停 + 告警** |

全局熔断是最重要的一条。连续失败意味着物料段已经污染，此时继续跑只是把剩下的干净物料一起烧掉。停比跑有价值。

**必须区分信号类型**，否则限速会被噪音带偏：

- 网络超时、代理 5xx、本地资源不足 → **不是风险信号**，不降速，重试即可
- 风控页、验证码、被要求验证、注册接口行为变化 → **是风险信号**，降速 + 冷却

把两者混在一起，会出现"代理抖动导致全局降速到 1"的假故障。

---

## 5. 养号引擎

回答"开通订阅前每隔几天聊天养号"。先纠正定位：养号不是为了"看起来活跃"，是两个真实目的：

1. 建立正常 usage baseline，让后续订阅 + 高频调用在这个账号历史里不突兀
2. **提前发现这个号已经死了** —— 这是养号的经济价值所在。没有养号，你会把订阅费花在死号上

### 节奏

| 阶段 | 频率 | 内容 |
|---|---|---|
| D0 | 注册后立即 1 次 | 极短对话，1–2 轮，兼作首用验证 |
| D1–D3 | 每 1–2 天 | 短对话，1–2 轮 |
| D4–D7 | 每 2–3 天 | 中长对话，3–5 轮 |
| D7+ | 每 2–3 天维持 | 中长对话，直到订阅 |
| 订阅后 | ramp-up | 前 3 天低量，逐步升到目标负载，**不要订阅当天冲高** |

时段必须落在账号时区的工作时间内，且禁止整点集体触发——批量特征是比指纹更容易被统计发现的东西。

### 内容多样性（关键）

N 个号问同一句话，是**比不养号更强的指纹**。话术层需要：

- 话术池 + 主题分布（技术、日常、写作、翻译、代码、闲聊各占比例）
- 长度分布（不要全是短句，也不要全是长文）
- persona 与 bundle 稳定绑定（不同账号表现出不同语气、职业背景、表达习惯，且长期一致）
- 模板变量注入，避免高度相似句式

### 架构判断：养号不要走 sub2api

走**独立浏览器通道**（Playwright 直连 claude.ai web），与 sub2api 物理隔离。两个理由：

1. sub2api 把养号流量当业务流量，会污染用量统计、限流判定和调度决策
2. **更致命**：sub2api 的身份注入特征会打在养号流量上，等于主动把 sub2api 的指纹烙进本该干净的账号。养号的意义就是让账号在干净通道里长得像真人，用 sub2api 养号是反的

### 负反馈回写

D1 就登不上，说明**注册环节就被标记了**，问题不在号，在物料。这时要回写到 `proxy / subnet / asn / email_domain` 的风险分上，而不是只把这个号标死。这是从"处理个体"升级到"处理源头"的关键动作。

---

## 6. 封号判定与级联移除

回答"检测到被封就移除账号和邮箱"。**反对"检测到就删"。**

### 分级判定

| 级别 | 证据 | 处置 |
|---|---|---|
| L0 正常 | 调用成功 | 正常 |
| L1 令牌失效 | refresh 失败 / 401 | 先 `POST /:id/refresh`；刷不动再降级 |
| L2 限流 | 429 / `rate_limit` | **不是封号**！仅临时不可调度 + 退避，到点恢复 |
| L3 受限 | 403 / 风控页 / 功能被封 | 置 `schedulable:false`，进观察队列，需多次确认才降级 |
| L4 需验证 | 要求手机/邮箱验证 | **进人工队列，不要删** —— 可能能救回来 |
| L5 永久封禁 | 明确封禁提示 / 账号不存在 | 移除 |

**429 绝不能当封号**。这是最常见的误杀，会把大量健康号删掉——尤其在限流高发期，一次误判就能清空号池。

### 移除级联（必须原子）

```
1. sub2api: POST /:id/schedulable {schedulable:false}     先止血，延迟最低
2. 观察期 24–72h                                         排除误判
3. sub2api: DELETE /:id（或保留 inactive 作为证据）
4. 墓碑写入：email → burned（永久）
             fingerprint → burned（永久）
             proxy / subnet / asn / email_domain → 风险分 +1，超阈值整段拉黑
5. 解绑并释放 sub2api 代理
6. 成本账本记沉没成本
```

**墓碑是防自杀循环的唯一手段。** 不加墓碑，下一轮补号会重新分配到刚死掉的 IP / 指纹 / 域名，新号上来就死，形成循环，而且你会误判为"注册器坏了"。

### 邮箱处理纠正

他说"账号和邮箱同时移除"，这里要改：

- catch-all 别名：删别名记录即可，但**地址本身进 burned，永不重用**
- **不要因为封号就删邮箱**。同一邮箱在其他号上是关联分析证据，删了等于丢掉溯源能力。改为"标记不可用"
- **域名级健康分**：某邮箱域封号率超标 → 整个域降权或停用。域名是比单个地址更重要的资产单位

---

## 7. 水位控制器

回答"保持号池够数量"。核心不是总量，是**分组水位 + 提前量**。

### 分组水位

```
desired = { aging: N1, warm: N2, subscribed: N3, reserve: N4 }
```

单一总数是错的：100 个"号"里可能 60 个还在养、10 个已订阅、30 个是死的，看总数看不出任何东西。

### 提前量（最容易被漏的算法点）

养号要 7–14 天。所以**不能用当前缺口补号**：

```
需要启动的注册数 = 目标水位
                 + 预期消耗速率 × lead_time
                 - 当前有效数
                 - 在途数（已注册未养完）

lead_time = 注册周期 + 养号周期
```

等到号池见底才补，补出来的号没养好就得上，等于白补——而且往往被迫用没养熟的号去订阅，订阅完秒封，钱和号一起损失。

### 控制器行为

- 缺口 > 0 → 生成注册任务（受 §4 的全局速率约束）
- **封号率异常 → 停止补号 + 告警**。封号潮说明物料已污染，此时补号是火上浇油
- 成本闸门：订阅要花钱，补号/订阅前检查预算上限
- 爆炸半径：单轮补号上限 + 每日注册上限（防止一次配置错误烧穿整个物料池）

---

## 8. 状态机

```
Bundle:   allocated → active → burned

Account:  PENDING       待注册
          REGISTERING   注册中
          REGISTERED    注册成功，待同步
          SYNCED        已同步 sub2api
          AGING         养号中（D0–D7）
          WARM          养号完成，可订阅
          SUBSCRIBING   订阅中
          SUBSCRIBED    已订阅，ramp-up
          ACTIVE        正常服务
          COOLDOWN      限流，临时不可调度
          OBSERVING     疑似受限，观察期
          MANUAL        待人工介入
          BURNED        确认死亡（终态）
```

`MANUAL` 是必须有的档位。总有一部分需要人工过验证，没有这个档位它们要么卡死流水线，要么被误判为死亡。

---

## 9. 你没考虑到的（按优先级）

1. **sub2api 自身指纹是命门**。审计已证实固定身份注入（`python__sub2api`、确定性 UUID、盐值会话 ID）。所有号走同一个 sub2api 出口，上游看到的是一坨同源流量。**号池做到极致，这一层不收口等于零**。优先级高于本方案其他全部内容。

2. **支付关联**。多号共用同一张卡 / 同一支付渠道 = 一次全灭。这比指纹更致命，而且技术上更难隔离。需要支付渠道池 + 每渠道绑定号数上限 + 渠道级封号率监控。

3. **429 ≠ 封号**。误杀重灾区，见 §6。

4. **墓碑防自杀循环**。见 §6，不加墓碑会陷入"新号上来就死"的循环。

5. **养号需要提前量**。见 §7，等见底才补必然翻车。

6. **养号通道必须与 sub2api 隔离**。见 §5，用 sub2api 养号是反向操作。

7. **凭证过期前保活**。长期不用的号，refresh_token 会失效。养号顺带实现了保活，但对"暂不使用的存量号"要有显式的低活跃保活任务（定期 refresh + 一次轻调用），否则几个月后要用时发现全挂了。

8. **订阅时序**。不要在养号期间订阅（订阅行为本身是信号）；不要在订阅当天冲负载；订阅后要 ramp-up。

9. **物料池健康投票**。不只删号，要给 proxy / subnet / asn / email_domain 打分并自动拉黑超标段。见 §5、§6。

10. **成本账本与 ROI**。每个号花了多少（代理 + 邮箱 + 订阅），产出多少。100 个号订阅是大额支出，没有账就无法决定续订、停订、优先级。

11. **爆炸半径控制**。单次补号上限、每日注册上限、全局熔断。防止一次参数错误烧穿物料池。

12. **上游契约变更隔离**。Claude 改注册流程 → 注册器全废。要 provider 抽象 + 灰度（新流程先跑 1 个验证通过再放量），否则每次上游改动都是一次全量停摆。

13. **人工兜底队列**。手机验证、复杂 captcha 必然存在。要有待人工面板 + 列表，而不是让它们卡死或被判死。

14. **号的价值分级**。订阅档位、账号年龄、剩余额度、历史健康度决定价值。调度与续订都要能按价值排序，不能用一个池子平等对待。

15. **凭证明文与审计**。sessionKey 和 OAuth token 等价于账号控制权，本地库必须加密（信封密钥 + kid 轮换），访问要审计。别让整个号池的钥匙躺在明文 SQLite 里。

16. **多维可观测性**。`job_id / bundle_id / sub2api_account_id` 贯通全链路；封号率要能按 proxy 段、email 域、注册批次下钻。否则你只知道"死了 30 个号"，不知道"是哪个物料段坏了"——而这个才是唯一有价值的结论。

---

## 10. 落地顺序

| 序 | 内容 | 前置依赖 |
|---|---|---|
| 1 | 收口 sub2api 指纹收敛 | 无（最高优先级） |
| 2 | 物料层 + 一致性域分配器（代理池/邮箱池/指纹池/墓碑） | 1 |
| 3 | 半自动注册 → `cookie-auth` → CRS pull 通路打通（端到端 1 个号） | 2 |
| 4 | 养号引擎（独立通道）+ 封号分级判定 + 墓碑级联 | 3 |
| 5 | 水位控制器 + 自适应限速 + 成本账本 | 4 |
| 6 | 对账、可观测性、待人工面板 | 5 |

第 3 步刻意做成**半自动**（跑到验证码停下等人工确认）。先验证链路无风控，再逐步去掉人工介入。全自动只有在链路已验证干净之后才有意义，否则你只是把失败放大。

---

## 11. 实现补充（实测结论，2026-09-30）

本节是落地过程中实测得到的结论，与上文同等效力。**踩过的坑写在这里，不要重新试。**

### 11.1 订阅判定：读计费页，不走 OAuth 也不猜接口

实测 `https://claude.ai/settings/billing`：

- 免费号渲染 `heading "Free plan"` + `link "Upgrade plan"`
- 付费号对应位置是 `Pro plan` / `Max plan`，且没有升级链接

这是官方明示，比任何接口字段都可靠。**已证伪的判据**：

| 判据 | 为什么不行 |
|---|---|
| `routingHint` 里的 `has_membership` | **免费号同样是 true**，它只表示"属于某个 org" |
| `/api/organizations` 的 `raven_type` / `capabilities` | 需要猜字段含义；且 sub2api 的 `refresh-tier` 已专做此事 |

**URL 坑**：SPA 用 hash 路由。`/settings` 和 `/settings/subscription` 都会落到
`#settings/general`，**只有 `/settings/billing` 是计费页**。判 URL 要看整串而非 path。

### 11.2 OAuth 的时机：订阅后 D1–D3，不能更早

授权 Claude Code 这个动作**真人也会做**（买了订阅装个命令行工具），所以动作本身不可疑，
**可疑的是时机**：

- **不能注册时做**：免费号必定失败（`permission_error`），而"刚注册 3 秒就想授权"
  这个行为本身就记录在案，等于自报是机器
- **不能养号期间做**：同样是免费状态，必然失败
- **不能订阅当天做**：§9-8 已定"订阅当天不要冲负载"

**推荐时序**：订阅生效后隔 1–3 天授权，授权后**紧接着做一次真实调用**
（"装完试试"）。授权了却从不调用，比不授权更扎眼。

三条配套约束：**一个号只授权一次**（token 可续期，反复授权不像人）；
**必须用注册时那个 bundle**（§3 全阶段不变）；**授权后纳入低频保活**（否则 refresh token 过期）。

### 11.3 执行侧的边界：无状态，不认识"冷却"和"并发"

**执行侧 = 接到一个任务、跑完、交回结果。** 它不持有账号生命周期，不认识号池统计规则。

```
服务端（Go）                     执行侧（Python）
├─ 并发锁                        ├─ 一次调用 = 一个任务
├─ 注册冷却（按出口）             ├─ 无 cooldown / 无 concurrency / 无 retry
├─ 号池状态机 + PG/SQLite        ├─ 平台适配器平行可插拔
├─ 重试与退避                     ├─ core 是平台无关的共享能力
└─ 出口 IP 分配                  └─ 只回报观测事实，不裁决
```

契约是「吃 spec、吐 result」：`executor/contracts/job.py` 里**刻意没有**
cooldown / concurrency / retry 字段。

`failed` 与 `blocked` 必须分开：`failed` 值得原样重试（如邮件没等到），
`blocked` 重试无用、需换出口或人工（如 CF 挑战过不去）。这样服务端不必解析日志就能决策。

**运行时可留、业务状态不可留**：`bundles/<name>/` 下的 profile / 指纹 / 字体子集引擎
是运行必需（cookie 不可重建），属于执行侧本地；而生命周期、订阅、封号、养号次数
属服务端。`meta.json` 只存运行时元数据，不承担状态语义。

### 11.4 注册链路的关键坑（都会伪装成"CF 拦截"或"页面改版"）

**一、不能连续导航同一个 URL。** `claude.ai/` 对未登录用户会 302 到 `/login`，
若之后又 `goto("/login")`，**第二次导航会把第一次正在初始化的 SPA 整个中止**。
症状是 `readyState` 永远 `loading`、a11y 树为空、DOM 只有一个 165 元素的骨架，
外加一堆 `NS_BINDING_ABORTED`——**看起来像 CF 拦截或页面改版，其实是自己打断自己**。
对应地：`wait_until` 要用 `"load"` 而非 `"commit"`。

**二、`page.keyboard.type()` 会静默失败。** 它是**页面级**键盘事件，依赖焦点恰好落在
输入框上；被覆盖层或 React 受控组件吃掉焦点时，字会打空且**不报任何错**，
直到提交才吐一句 `Email address is required`。**必须用元素级输入**
（`locator.press_sequentially()`）并在填完后 `input_value()` 自查。

**三、就绪判据不能看标题。** CF 挑战页的标题**也是 `Claude`**。可靠判据是
a11y 树里不出现 `verification` 且 role 种类 ≥ 2–3（说明 React 已挂载）。

**四、点击抛异常 ≠ 没点到。** Playwright 常在事件已派发之后才因元素位移/遮挡报超时。
若当失败吞掉，会出现"页面明明推进了却记成无动作"的矛盾。异常要如实记录。

**五、验证邮件是魔术链接，不是验证码。** 页面写 `To continue, click the link sent to …`，
邮件主题 `Your secure link to Claude.ai is here`，链接形如
`https://claude.ai/magic-link#<token>:<base64 邮箱>`。

**token 在 fragment 里**（`#` 之后），fragment 不随请求发给服务器、只能由页面 JS 读取，
**所以必须在同一 profile 内用真实浏览器打开**。页面上另有 `Enter verification code`
按钮，那是**备选路径**，默认不走。实现要两条都留、以邮件正文实际内容为准。

### 11.5 执行侧的其它实测

**字体子集是 canvas 区分的可行手段。** camoufox 0.5.7 移除了 `canvas:seed`
（维护者认为 canvas noise 本身是负债），所以同一台机器上跑出的账号 canvas 哈希
**字节级相同**——这是跨账号的强聚类特征。解法不是加噪声，而是让**字体环境不同**：
camoufox 读引擎 bundle 里的 `fontconfig/{os}/fonts.conf` 并**覆盖** `FONTCONFIG_FILE`，
所以切入点是引擎的 `fonts/` 目录。用硬链接树复制引擎、摘掉不同西文字族，
实测两个子集的 canvas 哈希不同（摘要见 `executor/core/font_subset.py`）。

**版本号里的 `beta` 是命名习惯，不是渠道信号。** camoufox 所有版本都带 `beta`，
真正的渠道标注在 `camoufox list` 里（`stable` / `prerelease`）。当前用
`v152.0.4-beta.31 (stable)`。`156` 换了字体目录布局（`fonts/W` 而非 `fonts/windows`）。

**profile 与引擎版本必须配套。** 用 152 打开 156 创建的 profile 会卡在兼容性迁移上
（`compatibility.ini` 里的 `LastVersion` 是判据）。降级不可行，要么同版本，要么重建。

