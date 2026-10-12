# OAuth：Go 原生执行

实现：`backend/internal/claude/oauth.go`。入口：`scripts/accountpool run ACCOUNT --steps oauth`。

1. Go Runner 取得执行锁，遵守任务冷却，通过 Python 的 `session` 步骤读取当前 Firefox Cookie。
2. Go 请求组织列表，显式组织优先；缺失的显式组织报错。未指定时优先 team，否则第一个。
3. Go 生成 PKCE S256 与随机 state，请求授权码。
4. 验证回调 scheme、host、path、单一 code/state，并恒定时间比较返回 state。
5. Go 向 token 端点提交授权码、state 和 verifier；不携带浏览器 Cookie，不自动重试。
6. 检查 access/refresh、有效期、scope、组织一致性，返回本次结果；Go 统一事务提交凭据与状态。

本地到期时间存毫秒；CRS 导出转换为 Unix 秒。授权成功表示取得 OAuth 凭据，不表示已经同步到 sub2api，因此状态为 subscribed，而非 synced。

账户权限不足时返回 `blocked:not-subscribed`。这属于权限结果，不通过重复请求解决。HTTP 401、429、Cloudflare challenge 与普通服务错误分别报告。异常响应体与 token 不写入诊断日志。

本地测试包含完整的模拟授权、PKCE/state 校验、错误回调、缺失凭据、失败不重发以及 Go→Python 当前会话读取。真实权限账号的兑换、下游导入和刷新仍需单独验收。

## sub2api 对照与自动交接

对照本地 sub2api 源码提交 `efe9aab1e4ec89a42ba45e8dac20e882c5409a6a`：
`backend/internal/service/oauth_service.go` 的 CookieAuth，
`backend/internal/repository/claude_oauth_service.go` 的授权/兑换，
`backend/internal/pkg/oauth/oauth.go` 的端点、客户端和 ScopeAPI。
内部 API 使用 ScopeAPI，不包含浏览器授权页专用的 `org:create_api_key`。
Token 响应按该源码读取 `organization.uuid`，并与所选组织核对。

维护器在成功健康检查发现订阅时写入 subscribed，排除免费池水位，派发一次 OAuth。
完整凭据取得后交给 CRS 导出，刷新由 sub2api 负责。兑换失败不会反复重放；已订阅但兑换失败的账号也不会重新计入免费池。
