"""平台适配器协议与延迟加载注册表。通用执行依赖位于 executor.runtime。"""

from __future__ import annotations

from typing import Protocol

from executor.contracts.job import JobResult, JobSpec


class Platform(Protocol):
    """一个平台要提供的全部东西。"""

    name: str
    # 该平台支持的步骤名。服务端据此校验 spec 是否合法。
    supported_steps: tuple[str, ...]

    def run(self, spec: JobSpec, workdir) -> JobResult:
        """按 spec.steps 顺序执行，返回结果。

        约定：
          - 不自己 sleep 等冷却，不做并发控制 —— 那是服务端的职责
          - 不吞异常：失败要落成 JobResult(status="failed"/"blocked")，
            让服务端知道停在哪一步、能不能重试
          - 幂等性尽力而为：重复跑同一步不应产生重复账号
        """
        ...


# 平台注册表。key 是 spec.platform 的取值。
REGISTRY: dict[str, str] = {
    "claude": "executor.platforms.claude.adapter:ClaudePlatform",
}


def load_platform(name: str) -> Platform:
    """按名字加载平台适配器。延迟导入，避免拉一个平台就装全平台的依赖。"""
    import importlib

    ref = REGISTRY.get(name)
    if not ref:
        raise SystemExit(f"没有这个平台适配器: {name!r}（可选: {', '.join(REGISTRY)}）")
    mod_name, _, cls_name = ref.partition(":")
    mod = importlib.import_module(mod_name)
    return getattr(mod, cls_name)()
