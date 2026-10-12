#!/usr/bin/env python3
"""accountpool 独立 Docker 部署；默认只启动 API 和管理台。"""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import sqlite3
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]

def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)

def prepare(root, directory, api_port=8787):
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    config = directory / "config.toml"
    env = directory / ".env"
    if not config.exists():
        shutil.copy2(root / "config.example.toml", config)
        config.chmod(0o600)
    if not env.exists():
        base = (root / ".env.example").read_text()
        base = base.replace("ACCOUNTPOOL_SETUP_KEY=", "ACCOUNTPOOL_SETUP_KEY=" + secrets.token_hex(32), 1)
        base = base.replace("ACCOUNTPOOL_PORT=8787", f"ACCOUNTPOOL_PORT={api_port}")
        base += f"\nACCOUNTPOOL_ENV_FILE={env}\nACCOUNTPOOL_CONFIG={config}\nACCOUNTPOOL_DATA_DIR={directory / 'data'}\n"
        with env.open("x") as stream:
            stream.write(base)
        env.chmod(0o600)
    for name in ("backend", "bundles", "artifacts", ".subsets"):
        (directory / "data" / name).mkdir(parents=True, exist_ok=True, mode=0o700)
    return env

def database(directory):
    path = directory / "data/backend/pool.db"
    if not path.exists():
        raise RuntimeError("请先启动管理台，并在代理池配置远端可用的代理")
    return sqlite3.connect(f"file:{path}?mode=ro", uri=True)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", nargs="?", choices=("up", "prepare", "check", "enable-worker", "stop-worker", "status"), default="up")
    parser.add_argument("--directory", type=Path, default=ROOT / ".deployment")
    parser.add_argument("--api-port", type=int, default=8787)
    parser.add_argument("--load-images", type=Path)
    args = parser.parse_args()
    if not 1 <= args.api_port <= 65535:
        parser.error("监听端口必须有效且彼此不同")
    directory = args.directory.resolve()
    env = prepare(ROOT, directory, args.api_port)
    compose = ["docker", "compose", "-p", "accountpool", "--env-file", str(env), "-f", str(ROOT / "compose.yaml")]
    if args.action == "prepare":
        print("配置已准备：" + str(directory) + "；未启动容器")
        return
    run(["docker", "info"], stdout=subprocess.DEVNULL)
    run(compose + ["config", "--quiet"])
    if args.action == "check":
        run(compose + ["config", "--services"])
        print("Compose 校验通过；未访问 Claude")
        return
    if args.action == "status":
        run(compose + ["--profile", "worker", "ps"])
        return
    if args.action == "stop-worker":
        run(compose + ["--profile", "worker", "stop", "-t", "120", "maintenance"])
        return
    if args.action == "enable-worker":
        with database(directory) as db:
            if db.execute("SELECT COUNT(*) FROM jobs WHERE state='running'").fetchone()[0]:
                raise RuntimeError("当前任务仍在执行，请等待完成")
            proxies = db.execute("SELECT id FROM proxies WHERE state='available' AND (cooldown_until IS NULL OR cooldown_until<datetime('now','localtime')) ORDER BY id").fetchall()
        running = run(compose + ["--profile", "worker", "ps", "--status", "running", "-q", "maintenance"], capture_output=True, text=True).stdout.strip()
        if running:
            raise RuntimeError("自动维护已启动，请先停止再重新检测代理")
        for (proxy_id,) in proxies:
            run(compose + ["run", "--rm", "--no-deps", "maintenance", "proxy", "test", str(proxy_id)])
        run(compose + ["--profile", "worker", "up", "-d", "--no-build", "--pull", "never", "maintenance"])
        print("已启用自动维护：绑定账号使用已检测代理，未绑定账号直连")
        return
    # Never recreate a running worker as a side effect of preparing the UI.
    worker = run(compose + ["--profile", "worker", "ps", "--status", "running", "-q", "maintenance"], capture_output=True, text=True).stdout.strip()
    if worker:
        raise RuntimeError("自动维护正在运行，请先完成当前任务并停止维护再部署")
    if args.load_images:
        run(["docker", "load", "-i", str(args.load_images.resolve())])
    model = json.loads(run(compose + ["config", "--format", "json"], capture_output=True, text=True).stdout)
    for service in ("api",):
        run(["docker", "image", "inspect", model["services"][service]["image"]], stdout=subprocess.DEVNULL)
    # Assign only the isolated deployment data tree to the application's UID.
    run(["docker", "run", "--rm", "--user", "0", "--network", "none",
         "--entrypoint", "chown", "-v", f"{directory / 'data'}:/state",
         model["services"]["api"]["image"], "-R", "1000:1000", "/state"])
    run(compose + ["up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "120", "api"])
    print("API 和管理台已通过健康检查；自动维护未启动")
    print("通过 SSH 转发访问管理台。初始化密钥位于：" + str(env))
    print("确认账号代理设置（可不配置，直接连接）后执行：scripts/deploy.sh enable-worker --directory " + str(directory))

if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError) as error:
        print("部署未完成：" + str(error), file=sys.stderr)
        sys.exit(1)
