# 管理台部署与验证

## 目录与启动

accountpool 与 accountpool-web 放在同一父目录。保留 config.toml 和 .env：
前者仍提供浏览器运行参数；后者仅为部署及 CLI 配置。网页供给配置保存在 SQLite。

在 accountpool 目录：
```sh
docker compose up -d --build
docker compose logs api
```

默认网页 http://localhost:5188。首次 API 日志包含一次初始化密钥，在网页设置自己的管理员用户名和至少 12 字符的密码。
也可以启动前设置 ACCOUNTPOOL_SETUP_KEY。初始化只允许一次，重启不会覆盖管理员。
初始化完成后，先配置邮箱和代理，确认无误再设置免费池目标。默认目标为 0。
修改参数在下一任务前生效，已开始的任务继续使用原配置。

## 会话与权限

管理台使用单管理员账户、密码哈希和持久化的服务端会话（24 小时）。
浏览器只持有 HttpOnly Cookie；数据库只保存会话令牌摘要。
写请求校验 Origin。反向代理必须保留外部 Host。
公网部署应在 web 前配置 TLS，并确保传递的协议来自可信入口；默认 Compose 仅绑定回环地址。

CRS 导出继续使用 ACCOUNTPOOL_API_USER / ACCOUNTPOOL_API_PASSWORD，未配置时导出登录关闭；
CRS Bearer token 不授予管理台权限。邮箱管理员密码只用于邮箱服务。
邮箱与代理凭据存放在应用数据库中，备份及持久卷应仅由服务管理员读取；GET 接口不返回明文凭据。

## 密码恢复

登录后配置页支持修改管理台密码，修改后撤销全部会话。
忘记密码时，通过容器内的本地命令恢复：
```sh
docker compose exec -T api /app/bin/accountpool admin-reset < /path/to/private-password.json
```
文件格式为包含 password 的 JSON 对象；输入文件应限制为管理员可读，使用后删除。
命令不接受密码参数，不输出密码，不修改初始化状态。

## 验收范围

使用独立数据库验证初始化、登录、刷新会话、登出、配置持久化和代理登记。
这些检查不启动真实注册，也不代表浏览器执行器已完成上游登录验收。
只有 api / web 健康并且 maintenance 运行正常后，才算部署完成。

## 自动维护

配置中心可设置健康检查周期（默认 1 小时），并启用定时网页消息、修改发送间隔和内容。每次只发送一条；成功后按间隔调度。明确失败进入冷却重试，连续失败指数退避，最长 24 小时。提交结果未知的消息不自动重发，以避免重复发送。OAuth 账号不参与免费账号维护。

复用本机已有数据库、浏览器资料和宿主代理时（Linux），使用 `docker compose -f deploy/compose.local-maintenance.yaml up -d`。该文件使用宿主网络，默认时区 Asia/Shanghai；应与原数据库写入时区一致。全新部署继续使用仓库根目录的 compose.yaml。两者只选一个维护器。


## 代理边界

应用和容器默认不配置全局代理。管理台、数据库和邮箱服务不继承账号代理。
账号代理是可选项：绑定代理时由任务携带指定出口，未绑定时直接连接。
已绑定代理不可用时等待，不回退直连。
本地环境单独设置 ACCOUNTPOOL_REQUIRE_PROXY=true，保留禁止裸连保护；远端默认不设置此变量。
先在代理管理登记，再通过 `proxy test ID` 检测 HTTPS 连通性（访问 example.com，不访问 Claude）。
测试通过后绑定账号；也可选择“不使用代理（直连）”。修改前先暂停维护。
