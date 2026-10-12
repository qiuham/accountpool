"""Browser job contract. Go owns lifecycle, HTTP operations and persistence.

Python performs browser steps and reads current profile cookies. Go-provided
file descriptors carry the shared execution lease and task-scoped mail IPC.
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any, Literal

# 执行步骤。平台适配器自行决定支持哪些。
Step = Literal["register", "onboard", "warmup", "session"]

# 结果状态。失败时 failedStep 指明停在哪，便于服务端决策是否重试。
Status = Literal["ok", "failed", "blocked"]


@dataclass
class Credentials:
    """执行产出的凭据。不同平台字段不同，统一放这里。"""

    session_key: str | None = None
    org_id: str | None = None
    extra: dict[str, Any] = field(default_factory=dict)

    def to_json(self) -> dict:
        d = {"sessionKey": self.session_key, "orgId": self.org_id}
        d.update(self.extra)
        return {k: v for k, v in d.items() if v is not None and v != ""}


@dataclass
class JobSpec:
    """一个任务的输入。由服务端生成，执行侧只读。"""

    platform: str                      # "claude" / "codex" / ...
    account: str                       # 账号名，决定 bundle 目录
    steps: list[str]                   # 要跑的步骤
    email: str | None = None           # 由 Go 分配，执行侧不建邮箱
    proxy: str | None = None           # 出口，服务端分配（执行侧不自己选）
    fingerprint: dict | None = None    # 指定指纹；不给则新生成
    engine: str | None = None          # 指定引擎（字体子集副本路径）
    name: str | None = None            # 展示名
    bundle_root: str | None = None     # 默认 bundles/
    artifact_root: str | None = None   # 默认 artifacts/
    options: dict[str, Any] = field(default_factory=dict)

    def __post_init__(self):
        for name in ("platform", "account"):
            value = getattr(self, name)
            if not isinstance(value, str) or not value.strip():
                raise ValueError(f"{name} must be a non-empty string")
        if (not isinstance(self.steps, list) or not self.steps
                or any(not isinstance(step, str) or not step.strip() for step in self.steps)):
            raise ValueError("steps must be a non-empty list of non-empty strings")
        if not isinstance(self.options, dict):
            raise ValueError("options must be an object")

    @classmethod
    def from_json(cls, path: str | Path) -> "JobSpec":
        d = json.loads(Path(path).read_text())
        if not isinstance(d, dict):
            raise ValueError("job must be a JSON object")
        unknown = d.keys() - cls.__dataclass_fields__.keys()
        if unknown:
            raise ValueError("unknown job fields: " + ", ".join(sorted(unknown)))
        return cls(**d)

    def to_json(self) -> dict:
        return asdict(self)


@dataclass
class JobResult:
    """一个任务的结果。执行侧产出，服务端读取。"""

    platform: str
    account: str
    status: Status
    credentials: Credentials = field(default_factory=Credentials)
    email: str | None = None
    failed_step: str | None = None
    reason: str | None = None          # 失败原因，机器可判
    detail: str | None = None          # 人看的补充
    artifacts: str | None = None       # 本轮产物目录
    started_at: str | None = None
    finished_at: str | None = None

    def to_json(self) -> dict:
        d = asdict(self)
        d["credentials"] = self.credentials.to_json()
        return {k: v for k, v in d.items() if v is not None or k in ("platform", "account", "status")}

    def write_json(self, path: str | Path) -> None:
        Path(path).write_text(json.dumps(self.to_json(), indent=2, ensure_ascii=False),
                              encoding="utf-8")


def ok(platform: str, account: str, **kw) -> JobResult:
    return JobResult(platform=platform, account=account, status="ok", **kw)


def failed(platform: str, account: str, step: str, reason: str, **kw) -> JobResult:
    """失败。step + reason 是服务端决定「重试还是放弃」的依据。"""
    return JobResult(platform=platform, account=account, status="failed",
                     failed_step=step, reason=reason, **kw)


def blocked(platform: str, account: str, step: str, reason: str, **kw) -> JobResult:
    """需要人工介入或换出口才能继续（例如 CF 持续拦截、账号被封）。

    与 failed 的区别：failed 值得原样重试，blocked 重试无用。
    """
    return JobResult(platform=platform, account=account, status="blocked",
                     failed_step=step, reason=reason, **kw)
