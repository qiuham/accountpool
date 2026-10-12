# 合并部署

管理容器 api 内由 Go 同时提供 /login、/admin、/assets 与 API；监听 8787。
maintenance 是独立执行容器，复用同一镜像和状态卷。没有前端 Nginx 容器。
远端为保留原反代地址，ACCOUNTPOOL_PORT=5188；容器内仍为 8787。Caddy 8443 反代 127.0.0.1:5188，443 保留给现有服务。

前后端源码分别保留在 accountpool 和 accountpool-web 两个相邻目录。
构建：docker compose build api
构建使用 frontend additional_context；需要支持 additional_contexts 的 Docker Compose/BuildKit。
部署：scripts/deploy.sh up --directory /opt/accountpool/state
启用执行器：scripts/deploy.sh enable-worker --directory /opt/accountpool/state

登录后在配置中心查看执行器心跳、平台调度、执行中数量；心跳每 15 秒更新，45 秒未更新视为离线。
在线表示维护进程心跳正常，不代表浏览器或外部平台请求一定成功。
执行参数仍在配置中心的执行调度设置；不提供未登录的配置入口或 Docker socket 控制。
