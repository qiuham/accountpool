"""Browser adapter: registration, onboarding, conversation and session extraction.

Go owns task sequencing, mailbox operations, OAuth, account health and persistence.
The adapter returns only this invocation's browser observations.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from pathlib import Path
import sqlite3
from executor.platforms.claude.session_cookies import read_session_cookies

from executor.runtime.execution_guard import LOCK_FD_ENV
from executor.runtime.browser_services import RPC_FD_ENV

from executor.contracts.job import Credentials, JobResult, JobSpec, blocked, failed, ok

# 步骤名 -> (模块, 函数, 需要账号已在 bundle 里)
STEPS = {
    "register": ("executor.platforms.claude.registration", "execute_registration_step"),
    "onboard": ("executor.platforms.claude.onboarding", "execute_onboarding_step"),
    "warmup": ("executor.platforms.claude.conversation", "execute_conversation_step"),

}


class ClaudePlatform:
    name = "claude"
    supported_steps = (*STEPS, "session")

    # --- 对外入口 ---
    def run(self, spec: JobSpec, workdir: Path | None = None) -> JobResult:
        started = time.strftime("%F %T")
        workdir = workdir or Path.cwd()

        steps = spec.steps or ["register"]
        if steps == ["session"]:
            credentials = self._collect_credentials(spec, workdir)
            if not credentials.session_key:
                return blocked(self.name, spec.account, "session", "blocked:session-expired")
            return ok(self.name, spec.account, credentials=credentials)
        unknown = [s for s in steps if s not in STEPS]
        if unknown:
            return failed(self.name, spec.account, unknown[0], "unsupported-step",
                          detail=f"支持: {', '.join(self.supported_steps)}")

        if os.environ.get("ACCOUNTPOOL_REQUIRE_PROXY") == "true" and not (spec.proxy or "").strip():
            return blocked(self.name, spec.account, steps[0], "proxy-required")

        # 每步一个浏览器子进程，透传出口等由服务端分配的参数
        env = dict(os.environ)
        for key in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
            env.pop(key, None)
        env["PYTHONPATH"] = str(workdir)
        env["ACCOUNTPOOL_PROXY"] = spec.proxy or ""

        artifacts = None
        for step in steps:
            rc, out_dir, err = self._execute_step(step, spec, workdir, env)
            if out_dir:
                artifacts = out_dir
            if rc != 0:
                # CF 持续拦截或页面被判风控 → 原样重试无用，标 blocked
                if err and err.startswith("blocked:"):
                    return blocked(self.name, spec.account, step, err,
                                   email=spec.email, artifacts=artifacts,
                                   started_at=started, finished_at=time.strftime("%F %T"))
                return failed(self.name, spec.account, step, err or f"exit-{rc}",
                              email=spec.email, artifacts=artifacts,
                              started_at=started, finished_at=time.strftime("%F %T"))

        return ok(self.name, spec.account, email=spec.email, artifacts=artifacts,
                  credentials=self._collect_credentials(spec, workdir),
                  started_at=started, finished_at=time.strftime("%F %T"))

    # --- 内部 ---
    def _execute_step(self, step: str, spec: JobSpec, workdir: Path, env: dict):
        mod_name, fn_name = STEPS[step]
        argv = self._build_step_arguments(step, spec)

        # 子进程方式：环境变量对 config 生效（它在新进程里重新读），
        # 且单步崩溃不会带走整条链
        code = (
            "import importlib,json,sys;"
            f"fn=getattr(importlib.import_module({mod_name!r}),{fn_name!r});"
            "sys.exit(fn(json.load(sys.stdin)))"
        )
        print(f"\n{'='*60}\n[{self.name}/{step}] {' '.join(argv)}\n{'='*60}", flush=True)
        started_ns = time.time_ns()
        p = subprocess.run([sys.executable, "-u", "-c", code], cwd=str(workdir),
                           env=env, input=json.dumps(argv), text=True,
                           pass_fds=tuple(int(env[key]) for key in (LOCK_FD_ENV, RPC_FD_ENV) if key in env))

        out_dir = self._find_current_step_artifacts(spec, step, workdir, since_ns=started_ns)
        err = None
        if p.returncode != 0:
            err = self._classify_step_failure(spec, out_dir)
        return p.returncode, out_dir, err

    def _build_step_arguments(self, step: str, spec: JobSpec) -> list[str]:
        a = ["--account", spec.account]
        if step == "register":
            if spec.email:
                a += ["--email", spec.email]
            if spec.fingerprint:
                a += ["--fingerprint", str(spec.fingerprint)]
        if spec.engine and step == "register":
            a += ["--executable-path", spec.engine]
        if spec.name and step == "onboard":
            a += ["--name", spec.name]
        if spec.name and step == "warmup":
            a += ["--my-name", spec.name]
        if step == "warmup":
            a += ["--turns", "1"]
            if spec.options.get("prompt"):
                a += ["--prompt", spec.options["prompt"]]
        return a

    def _find_current_step_artifacts(self, spec: JobSpec, step: str, workdir: Path, *,
                         since_ns: int = 0) -> str | None:
        root = workdir / "artifacts"
        if not root.is_dir():
            return None
        prefix = {"register": "register-", "onboard": "onboarding-",
                  "warmup": "warmup-"}[step]
        dirs = [d for d in root.glob(f"{prefix}{spec.account}-*")
                if d.is_dir() and d.stat().st_mtime_ns >= since_ns]
        return str(max(dirs, key=lambda d: d.stat().st_mtime)) if dirs else None

    def _classify_step_failure(self, spec: JobSpec, out_dir: str | None) -> str:
        """把失败归类成机器可判的 reason，供服务端决策。"""

        # 没有报告不构成账号或浏览器资料缺失的证据。
        if not (out_dir and os.path.isdir(out_dir)):
            return "no-artifacts"
        rep = Path(out_dir) / "report.json"
        if not rep.is_file():
            return "crashed-before-report"
        try:
            d = json.loads(rep.read_text())
        except Exception:
            return "bad-report"
        stop = d.get("stoppedAt")
        # 只有明确观察到挑战才标 CF；页面未就绪不证明遇到挑战。
        if stop in ("onboarding-required", "missing-chat-input", "tool-permission-pending", "personal-confirmation-required"):
            return stop
        if stop == "cf-challenge":
            if (d.get("pageState") or {}).get("human_action_required") is True:
                return "blocked:cf-challenge-human-required"
            return "blocked:cf-challenge-not-passed"
        if stop == "browser-verification-failed":
            return "blocked:browser-verification-failed"
        if stop == "spa-not-rendered":
            return "page-not-ready"
        if stop == "access-denied":
            return "blocked:access-denied"
        if stop == "http-error":
            if (d.get("pageState") or {}).get("http_status") == 429:
                return "blocked:rate-limited"
            return "page-http-error"
        if stop == "no-session-key":
            return "blocked:session-expired"
        if stop and stop.startswith("mailbox-"):
            return "blocked:" + stop
        if stop in ("mail-timeout",):
            return "mail-not-received"
        if stop in ("email-box-missing", "email-not-entered"):
            return f"blocked:{stop}"
        # 物料缺失：bundle 或会话不在本地。**重试无用**——不是执行失败，
        # 是"这个号在本地压根没准备好"，得先补物料或重新登录。
        if stop in ("no-session-key", "no-bundle"):
            return f"blocked:{stop}"

        # warmup 顺带产出的结论：账号被验证拦住，收不到回复
        if d.get("blocked"):
            return "blocked:" + str(d["blocked"]).replace("_", "-")

        return stop or "unknown"

    def _collect_credentials(self, spec: JobSpec, workdir: Path) -> Credentials:
        """从 bundle 的 meta/profile 里捞出凭据，回给服务端入库。"""
        bundle = workdir / "bundles" / spec.account
        cred = Credentials()
        meta = bundle / "meta.json"
        if meta.is_file():
            try:
                cred.extra["email"] = json.loads(meta.read_text()).get("email")
            except Exception:
                pass
        try:
            cookies = read_session_cookies(bundle)
            cred.session_key = cookies.get("sessionKey")
            cred.org_id = cookies.get("lastActiveOrg")
            if "routingHint" in cookies:
                cred.extra["routingHint"] = cookies["routingHint"]
        except sqlite3.Error:
            pass

        return cred
