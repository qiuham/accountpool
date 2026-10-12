#!/usr/bin/env python3
"""走完注册后的 onboarding：语义驱动，零 CSS 选择器，零 LLM。

四层
----
  1. 定位 —— get_by_role + 多语言别名正则。a11y 树里没有 class，DOM/样式
     重构不影响；可访问名称是无障碍合规要求，改它要付真实代价，所以比
     class 名稳。不写任何 hash 类名或 data-testid。
  2. 决策 —— 按 intent 优先级尝试，不靠 URL、不靠硬编码文案分支。
  3. 就绪 —— expect(...).to_be_enabled() 等按钮真正变亮，不用死 sleep。
  4. 回归 —— 每步把 aria_snapshot 落盘。前端改版后 diff 新旧快照，能直接
     看出是哪一步的树变了，改一行别名表即可，不必重新猜。

    python tools/onboarding.py --account claudetest --probe
    python tools/onboarding.py --account claudetest
"""

import argparse
import json
import random
import re
import sys
import time
import warnings
from pathlib import Path

warnings.filterwarnings("ignore")
from camoufox.sync_api import Camoufox  # noqa: E402
from playwright.sync_api import Error, expect  # noqa: E402

from executor.platforms.claude.onboarding_consents import check_required_onboarding_consents
from executor.runtime.config import CFG, camoufox_kwargs  # noqa: E402
from executor.platforms.claude.page_diagnostics import is_chat_page_ready, inspect_page, navigate_once
from executor.runtime.fingerprint import load_fingerprint  # noqa: E402

# --- 第 1 层：意图别名表 ---------------------------------------------------
# next 必须锚定：不锚定的话 "Continue with Google" / "Continue with email"
# 会被当成推进按钮点掉，那两条是登录方式，点了就偏航。
INTENT = {
    "next":     r"^\s*(Continuar|Continue|Next|Siguiente|Crear cuenta|Create account|继续|下一步)\s*$",
    "personal": r"(Para uso personal|For personal use|Personal use|个人)",
    "free":     r"^\s*(Usa Claude gratis|Use Claude for free|Start for free|Continue with Free|免费)\s*$",
    # 跳过类。必须覆盖 "Set up later" 这种带前缀的措辞——之前只锚定
    # "Later"，导致角色选择页的 "Set up later" 匹配不上，流程卡死。
    "skip":     r"(^\s*(Omitir|Skip|Not now|Maybe later|No thanks|Later|跳过)\s*$"
                r"|Configurar m[aá]s tarde|Set up later|Skip for now|Do this later|I'?ll do this later)",
}
ORDER = ("next", "personal", "free", "skip")


def capture_accessibility_snapshot(page) -> str:
    try:
        return page.locator("body").aria_snapshot()
    except Exception as e:
        return f"<aria 不可用: {type(e).__name__}>"


def count_visible_roles(page) -> dict:
    """可见元素的 role 分布，当「这是哪一页」的结构指纹用。"""
    try:
        return page.evaluate("""() => {
          const o = {};
          const sel = '[role],button,a[href],input,textarea,[contenteditable="true"]';
          for (const el of document.querySelectorAll(sel)) {
            if (!(el.offsetWidth || el.offsetHeight)) continue;
            const r = (el.getAttribute('role') || el.tagName.toLowerCase()).toLowerCase();
            if (r === 'presentation' || r === 'none') continue;
            o[r] = (o[r] || 0) + 1;
          }
          return o;
        }""")
    except Exception:
        return {}


def click_onboarding_action(page, intent: str) -> bool:
    """按 intent 点按钮/链接。

    点击抛异常不等于没点到：Playwright 常在事件已派发之后才因为元素位移、
    被遮挡或重渲染而报错。那种情况下页面其实已经前进了，若当失败上报，
    排查时会看到「明明推进了却记成无动作」的矛盾。所以异常要如实记下。
    """
    pattern = INTENT[intent]
    for role in ("button", "link"):
        loc = page.get_by_role(role, name=re.compile(pattern, re.I))
        try:
            n = loc.count()
        except Exception:
            continue
        for i in range(n):
            el = loc.nth(i)
            try:
                if not el.is_visible():
                    continue
            except Exception:
                continue
            try:
                expect(el).to_be_enabled(timeout=8000)
            except Exception:
                continue
            try:
                el.click()
                print(f"    → 点击 [{intent}]")
                return True
            except Exception as e:
                # 事件可能已经派发，交给调用方按页面是否变化来判断
                print(f"    ! [{intent}] 点击抛异常（可能已生效）: {type(e).__name__}")
                return True
    return False


def fill_first_empty_textbox(page, value) -> bool:
    """填第一个空的 textbox。按钮常处于 disabled，填完才亮。"""
    try:
        loc = page.get_by_role("textbox")
        for i in range(loc.count()):
            el = loc.nth(i)
            if not el.is_visible() or (el.input_value() or "").strip():
                continue
            # 元素级输入，不用 page.keyboard.type()（页面级，焦点不对就静默打空）
            el.click()
            el.press_sequentially(value, delay=random.randint(30, 80))
            print(f"    → 填入 textbox = {value!r}")
            page.wait_for_timeout(600)
            return True
    except Exception:
        pass
    return False


def execute_onboarding_step(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True, help="bundles/ 下的账号名，如 claudetest")
    ap.add_argument("--name", default=None, help="展示名；默认取 config 里的 display_name")
    ap.add_argument("--max-steps", type=int, default=14)
    ap.add_argument("--probe", action="store_true", help="只看结构，不点击")
    ap.add_argument("--headless", action="store_true")
    args = ap.parse_args(argv)
    name = args.name or CFG.identity.display_name

    bundle = CFG.paths.bundles / args.account
    if not bundle.is_dir():
        print(f"没有这个账号: {bundle}")
        return 1
    profile = bundle / "profile"
    fp_file = bundle / "fingerprint.json"
    if not profile.is_dir() or not fp_file.is_file():
        print(f"bundle 不完整（缺 profile 或 fingerprint.json）: {bundle}")
        return 1

    meta = {}
    if (bundle / "meta.json").is_file():
        meta = json.loads((bundle / "meta.json").read_text())

    out = CFG.paths.artifacts / f"onboarding-{args.account}-{int(time.time())}"
    out.mkdir(parents=True, exist_ok=True)
    snaps = out / "capture_accessibility_snapshot"
    snaps.mkdir(exist_ok=True)
    report = {"account": args.account, "email": meta.get("email"),
              "startedAt": time.strftime("%F %T"), "steps": []}

    print(f"账号   : {args.account}")
    print(f"展示名 : {name}")
    print(f"引擎   : {meta.get('engine') or '(默认)'}")

    with Camoufox(**camoufox_kwargs(load_fingerprint(fp_file), profile,
                                    engine=meta.get("engine"),
                                    headless=args.headless)) as ctx:
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        from executor.runtime.http_trace import attach_http_trace
        attach_http_trace(page)
        page.set_default_timeout(45000)

        # 唯一的导航。若 profile 已带着登录态，就直接落到 /new（它会按需
        # 跳到 /onboarding 或聊天页）；重复 goto 同一 URL 会中止正在初始化的 SPA。
        if page.url in ("about:blank", ""):
            state = navigate_once(page, "https://claude.ai/new")
        else:
            state = inspect_page(page)
        if state.kind in ("cf-challenge", "access-denied", "http-error", "navigation-timeout", "navigation-error"):
            report.update({"stoppedAt": state.kind, "pageState": state.to_json(), "finalUrl": page.url})
            (out / "report.json").write_text(json.dumps(report, indent=2, ensure_ascii=False))
            return 2 if state.human_action_required else 1
        # 等 CF + SPA 就绪。判据是 role 种类，不是标题
        deadline = time.time() + CFG.timing.cf_timeout
        while time.time() < deadline:
            page.wait_for_timeout(1500)
            snap = capture_accessibility_snapshot(page)
            state = inspect_page(page)
            if state.human_action_required:
                report["stoppedAt"] = "cf-challenge"
                break
            if len(count_visible_roles(page)) >= 2:
                break

        prev, stalled = None, 0
        for n in range(1, args.max_steps + 1):
            if inspect_page(page).human_action_required:
                report["stoppedAt"] = "cf-challenge"
                break
            snap = capture_accessibility_snapshot(page)
            counts = count_visible_roles(page)
            (snaps / f"step{n:02d}.yaml").write_text(snap, encoding="utf-8")
            print(f"\n--- 第 {n} 步 ---")
            print(f"  url={page.url}")
            print(f"  role={counts}")
            for line in snap.splitlines()[:10]:
                print(f"    {line}")
            rec = {"n": n, "url": page.url, "roles": counts}
            report["steps"].append(rec)

            if is_chat_page_ready(page):
                print("  ✓ 已进入聊天页")
                break
            if "/onboarding" not in page.url:
                report["stoppedAt"] = "unexpected-page"
                break

            if snap == prev:
                stalled += 1
                if stalled >= 3:
                    print("  停手：连续三轮无变化")
                    break
            else:
                stalled = 0
            prev = snap

            if args.probe:
                print("  --probe：不操作")
                continue

            # --- 第 2 层：意图决策 ---
            if check_required_onboarding_consents(page):
                rec["action"] = "check"
            elif fill_first_empty_textbox(page, name):
                rec["action"] = "fill"
            else:
                for intent in ORDER:
                    if click_onboarding_action(page, intent):
                        rec["action"] = f"click:{intent}"
                        # Wait for the SPA transition instead of immediately clicking again.
                        transition_deadline = time.monotonic() + 10
                        while time.monotonic() < transition_deadline:
                            page.wait_for_timeout(250)
                            if capture_accessibility_snapshot(page) != snap:
                                break
                        else:
                            report["stoppedAt"] = "onboarding-action-no-progress"
                        break
                else:
                    print("  ? 没有可识别的动作，再等一轮")
                    stalled += 1
                    page.wait_for_timeout(5000)
                    if stalled >= 3:
                        print("  停手：连续三轮无可识别动作")
                        break
                    continue

            if report.get("stoppedAt") == "onboarding-action-no-progress":
                break

        complete = is_chat_page_ready(page)
        report["completed"] = complete
        report["finalUrl"] = page.url
        if not complete and "stoppedAt" not in report:
            report["stoppedAt"] = "onboarding-incomplete"
        final = count_visible_roles(page)
        print(f"\n最终: {page.url}")
        print(f"  role={final}")
        try:
            (out / "final.html").write_text(page.content(), encoding="utf-8")
        except Exception:
            pass
        try:
            page.screenshot(path=str(out / "final.png"), full_page=True, timeout=20000)
        except Exception:
            pass

    (out / "report.json").write_text(json.dumps(report, indent=2, ensure_ascii=False),
                                     encoding="utf-8")
    print(f"\n产出: {out}")
    print(f"  aria 快照: {snaps}（改版时 diff 这里）")
    if report.get("stoppedAt") == "cf-challenge":
        return 2
    return 0 if complete else 1


if __name__ == "__main__":
    from executor.runtime.execution_guard import execution_guard

    with execution_guard():
        raise SystemExit(execute_onboarding_step())
