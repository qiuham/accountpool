#!/usr/bin/env python3
"""统一配置读取。所有脚本从 config.toml 取值，不再各自硬编码。

    from executor.runtime.config import CFG, PROXY, PREFS
    url = CFG.proxy.url
    print(PROXY)          # "http://127.0.0.1:7897"

环境变量可以覆盖，便于临时换代理或跑第二套环境而不动文件：
    ACCOUNTPOOL_PROXY, ACCOUNTPOOL_CONFIG
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass
from pathlib import Path

from camoufox.addons import DefaultAddons

# 本文件在 executor/runtime/ 下，项目根要往上三层
ROOT = Path(__file__).resolve().parent.parent.parent
CONFIG_FILE = Path(os.environ.get("ACCOUNTPOOL_CONFIG", ROOT / "config.toml"))

# 关掉 Firefox 首次启动的各种弹窗与上报。之前遇到过 profile 里残留
# .parentlock 导致有头模式弹会话恢复对话框、卡 5 分钟以上。
NO_DIALOG_PREFS = {
    "browser.sessionstore.resume_from_crash": False,
    "browser.sessionstore.max_resumed_crashes": 0,
    "browser.startup.page": 0,
    "browser.shell.checkDefaultBrowser": False,
    "browser.aboutwelcome.enabled": False,
    "datareporting.policy.dataSubmissionEnabled": False,
    "toolkit.telemetry.enabled": False,
    "browser.tabs.warnOnClose": False,
    "dom.disable_beforeunload": True,
    # One foreground task per browser; avoid speculative processes and retained
    # back/forward documents. Keep site isolation and the sandbox enabled.
    "dom.ipc.processCount": 2,
    "dom.ipc.processCount.webIsolated": 1,
    "dom.ipc.processPrelaunch.enabled": False,
    "browser.sessionhistory.max_total_viewers": 0,
    "browser.cache.memory.capacity": 32768,  # KiB: bounded network memory cache
    "image.cache.size": 16777216,  # bytes: decoded image cache

}


@dataclass(frozen=True)
class Proxy:
    url: str


@dataclass(frozen=True)
class Browser:
    os: str
    locale: str
    geoip: bool


@dataclass(frozen=True)
class Engine:
    subset_count: int
    channel: str


@dataclass(frozen=True)
class Timing:
    # 只保留执行侧自己需要的超时。冷却/并发/退避属于服务端的调度职责，
    # 执行侧不认识这些概念 —— 它只在被调用时跑完一次任务。
    cf_timeout: int
    code_timeout: int
    reply_timeout: int
    warmup_gap: int


@dataclass(frozen=True)
class Identity:
    display_name: str


@dataclass(frozen=True)
class Paths:
    bundles: Path
    artifacts: Path
    subsets: Path


@dataclass(frozen=True)
class Config:
    proxy: Proxy
    browser: Browser
    engine: Engine
    timing: Timing
    identity: Identity
    paths: Paths
    root: Path


def _load() -> Config:
    if not CONFIG_FILE.is_file():
        raise SystemExit(f"缺少配置文件: {CONFIG_FILE}")
    with CONFIG_FILE.open("rb") as f:
        raw = tomllib.load(f)

    b, e = raw.get("browser", {}), raw.get("engine", {})
    t, i, pa = raw.get("timing", {}), raw.get("identity", {}), raw.get("paths", {})

    # 环境变量优先，方便临时切代理而不改文件
    proxy_url = os.environ.get("ACCOUNTPOOL_PROXY", "")

    return Config(
        proxy=Proxy(proxy_url),
        browser=Browser(b.get("os", "windows"), b.get("locale", "en-US"),
                        bool(b.get("geoip", True))),
        engine=Engine(int(e.get("subset_count", 8)), e.get("channel", "official/stable")),
        timing=Timing(int(t.get("cf_timeout", 150)),
                      int(t.get("code_timeout", 180)), int(t.get("reply_timeout", 180)),
                      int(t.get("warmup_gap", 12))),
        identity=Identity(i.get("display_name", "Alex")),
        paths=Paths((ROOT / pa.get("bundles", "bundles")).resolve(),
                    (ROOT / pa.get("artifacts", "artifacts")).resolve(),
                    (ROOT / pa.get("subsets", ".subsets")).resolve()),
        root=ROOT,
    )


CFG = _load()

# 常用快捷引用，省得每处都写 CFG.xxx.yyy
PROXY = CFG.proxy.url
PREFS = NO_DIALOG_PREFS
BUNDLES = CFG.paths.bundles
ARTIFACTS = CFG.paths.artifacts
SUBSETS = CFG.paths.subsets


def camoufox_kwargs(fingerprint, profile, engine=None, headless=False):
    """拼一份 Camoufox 启动参数。

    代理 / geoip / locale / 关弹窗这些每处都一样，集中在这里，
    免得某个脚本漏了 geoip 导致时区跟 IP 对不上。
    """
    if os.environ.get("ACCOUNTPOOL_REQUIRE_PROXY") == "true" and not PROXY:
        raise ValueError("未分配账号代理，已阻止启动 Claude 浏览器")
    kw = dict(
        headless=headless,
        # Dedicated single-site executor does not need an ad-blocking extension
        # or its filter-list downloads and background WebExtensions process.
        exclude_addons=[DefaultAddons.UBO],
        os=CFG.browser.os,
        locale=CFG.browser.locale,
        fingerprint=fingerprint,
        geoip=CFG.browser.geoip,
        persistent_context=True,
        user_data_dir=str(profile),
        firefox_user_prefs=NO_DIALOG_PREFS.copy(),
        # 人类化行为：鼠标移动轨迹、点击节奏、打字停顿。
        #
        # 为什么必须有：CF 的挑战除了看指纹和 IP，还听行为信号。
        # 实测对比（同为全新 profile）：
        #   有 humanize → 42.4s 到 "Sign in - Claude"，邮箱框出现
        #   无 humanize → 10s 就没了挑战 token，但页面停在中间态，
        #                 75s 仍不见邮箱框 —— 也就是"看起来过了其实没进门"
        # 缺这个参数时的表现，跟之前记录的"卡在 CF 验证码"完全一致。
        humanize=True,
        # 抑制 camoufox 自己那些"你确定要用这个吗"的警告，避免污染日志
        i_know_what_im_doing=True,
    )
    if PROXY:
        kw["proxy"] = {"server": PROXY}
    if engine and Path(engine).is_file():
        kw["executable_path"] = str(engine)
    return kw


def subset_for(account_name: str) -> int:
    """按账号序号决定用哪份字体子集。"""
    import re
    m = re.search(r"(\d+)$", account_name or "")
    n = int(m.group(1)) if m else 0
    return n % CFG.engine.subset_count


def subset_engine(account_name: str) -> str | None:
    """账号对应的字体子集引擎路径；没生成就返回 None（用默认引擎）。"""
    exe = SUBSETS / str(subset_for(account_name)) / "camoufox"
    return str(exe.resolve()) if exe.is_file() else None


if __name__ == "__main__":
    print(f"配置文件: {CONFIG_FILE}")
    print(f"  代理    : {CFG.proxy.url}")
    print(f"  浏览器  : os={CFG.browser.os} locale={CFG.browser.locale} geoip={CFG.browser.geoip}")
    print(f"  引擎    : 渠道={CFG.engine.channel} 子集数={CFG.engine.subset_count}")
    print(f"  超时    : cf={CFG.timing.cf_timeout}s code={CFG.timing.code_timeout}s reply={CFG.timing.reply_timeout}s")
    print(f"  显示名  : {CFG.identity.display_name}")
    print(f"  目录    : bundles={CFG.paths.bundles.name} artifacts={CFG.paths.artifacts.name}")
