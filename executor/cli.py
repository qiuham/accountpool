#!/usr/bin/env python3
"""执行侧统一入口。

服务端（Go）调这一个命令，给一份 spec，拿一份 result：

    # 服务端写法：生成 spec → 调执行侧 → 读 result
    python -m executor run --job /tmp/job.json --out /tmp/result.json

    # 手工调试：不用写 spec 文件
    python -m executor run --platform claude --account claude001 \
        --steps register,onboard --email claude001@hamqiu.com

执行入口与 Go runner 共用执行锁；独立运行也遵守 60 秒冷却。
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path

from executor.runtime.execution_guard import execution_guard

from executor.contracts.job import JobResult, JobSpec
from executor.platforms.registry import load_platform


def handle_run_command(args) -> int:
    if args.job:
        spec = JobSpec.from_json(args.job)
    else:
        if not args.platform or not args.account:
            print("要么给 --job，要么给 --platform 和 --account")
            return 2
        steps = [s.strip() for s in (args.steps or "register").split(",") if s.strip()]
        fp = json.loads(Path(args.fingerprint).read_text()) if args.fingerprint else None
        opts = {}
        spec = JobSpec(platform=args.platform, account=args.account, steps=steps,
                       email=args.email, proxy=args.proxy, fingerprint=fp,
                       engine=args.engine, name=args.name, options=opts)

    platform = load_platform(spec.platform)
    print(f"平台 : {spec.platform}")
    print(f"账号 : {spec.account}")
    print(f"步骤 : {', '.join(spec.steps)}")
    print("出口 : " + ("任务已指定" if spec.proxy else "使用配置默认值"))

    workdir = Path(args.workdir).resolve() if args.workdir else Path.cwd()
    with execution_guard():
        result: JobResult = platform.run(spec, workdir=workdir)

    out = Path(args.out) if args.out else workdir / "artifacts" / f"result-{spec.account}.json"
    out.parent.mkdir(parents=True, exist_ok=True)
    result.write_json(out)

    mark = {"ok": "✅", "failed": "✗", "blocked": "⛔"}.get(result.status, "?")
    print(f"\n{mark} status={result.status}", end="")
    if result.failed_step:
        print(f"  failedStep={result.failed_step}  reason={result.reason}", end="")
    print()
    if result.credentials.session_key:
        print("   sessionKey: present")
    print(f"   result -> {out}")
    return 0 if result.status == "ok" else 1


def handle_platforms_command(_args) -> int:
    from executor.platforms.registry import REGISTRY
    for name in REGISTRY:
        p = load_platform(name)
        print(f"  {name:<10} steps: {', '.join(p.supported_steps)}")
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="executor", description="注册执行侧")
    sub = ap.add_subparsers(dest="cmd", required=True)

    r = sub.add_parser("run", help="跑一个任务")
    r.add_argument("--job", help="job spec JSON（服务端生成）")
    r.add_argument("--out", help="result 输出路径")
    r.add_argument("--platform", help="平台名，如 claude")
    r.add_argument("--account", help="账号名，如 claude001")
    r.add_argument("--steps", help="逗号分隔，如 register,onboard,warmup")
    r.add_argument("--email", help="服务端分配的邮箱")
    r.add_argument("--proxy", help="出口，通常由服务端分配")
    r.add_argument("--fingerprint", help="指定指纹 JSON")
    r.add_argument("--engine", help="指定引擎（字体子集副本路径）")
    r.add_argument("--name", help="展示名")
    r.add_argument("--workdir", help="工作目录，默认当前目录")
    r.set_defaults(func=handle_run_command)

    p = sub.add_parser("platforms", help="列出可用平台适配器")
    p.set_defaults(func=handle_platforms_command)

    args = ap.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
