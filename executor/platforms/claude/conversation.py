#!/usr/bin/env python3
"""养号：用已登录的 profile 打开 claude.ai 聊几句，让账号看起来是活的。

只做两件事——发消息、等回复、存档。不做任何账号操作，不碰 OAuth。

定位方式与 onboarding.py 一致，走无障碍树而不是 CSS：
  - 仅选择具有明确消息名称的唯一可编辑 textbox；搜索框与歧义候选不选。
  - 发送靠 Enter（空态没有 send 按钮，出现按钮反而是回复中的状态）。
  - 回复完成需 SSE 终止证据或本次新增回复的明确 UI 完成证据；
    工具授权、生成中、错误页面均不算完成。

    python tools/warmup.py --account claude001            # 按账号名跑
    python tools/warmup.py --account claude001 --turns 3  # 只发 3 轮
    python tools/warmup.py --account claude001 --probe    # 只看结构，不发
"""

import argparse
import json
import os
import random
import re
import sys
import time
import warnings
from pathlib import Path
from executor.platforms.claude.completion_observer import CompletionObserver
from executor.platforms.claude.reply_state import assistant_count, permission_pending, allow_web_read_once, current_reply_finished
from executor.platforms.claude.page_diagnostics import find_chat_input, has_visible_element

warnings.filterwarnings("ignore")
from playwright.sync_api import Error
from camoufox.sync_api import Camoufox  # noqa: E402

from executor.runtime.config import CFG, camoufox_kwargs  # noqa: E402
from executor.runtime.fingerprint import load_fingerprint  # noqa: E402

# 普通人会问的日常问题：别太技术、别太长、别像在测功能。
PROMPTS = [
    "Any tips for keeping basil alive indoors? Mine keeps wilting.",
    "What's a good way to remember the difference between its and it's?",
    "Why does coffee taste more bitter after it cools down?",
    "I have half a cabbage and some eggs. What should I make?",
    "How do I get rid of that musty smell in a closet?",
    "Is it worth learning to sharpen my own kitchen knives?",
    "What should I look for when buying a used bicycle?",
    "My sourdough comes out dense. What am I probably doing wrong?",
    "Any good stretches for someone who sits at a desk all day?",
    "How do I stop my phone from waking me up at night?",
    "What's an easy way to keep track of monthly expenses?",
    "Do houseplants actually help with indoor air quality?",
]


def capture_accessibility_snapshot(page) -> str:
    try:
        return page.locator("body").aria_snapshot()
    except Exception as e:
        return f"<aria 不可用: {type(e).__name__}>"


def wait_for_reply_completion(page, *, completion: CompletionObserver,
                              timeout: float = 180, baseline: int | None = None) -> tuple[bool, str]:
    """等待本次回复的网络或 UI 完成证据，并处理单次公开网页读取授权。"""
    deadline = time.monotonic() + timeout
    handled = set()
    def finish(ok):
        # Serialize the document only once, after the observed transition.
        snapshot = capture_accessibility_snapshot(page)
        return ok and not snapshot.startswith("<aria 不可用:"), snapshot
    try:
        while time.monotonic() < deadline:
            page.wait_for_timeout(min(200, max(0, (deadline - time.monotonic()) * 1000)))
            if completion.failure:
                return finish(False)
            if has_visible_element(page, '[role="alert"]'):
                return finish(False)
            if permission_pending(page):
                allow_web_read_once(page, handled)
                continue
            stop = page.get_by_role('button', name=re.compile(
                r'^(Stop response|Stop generating|停止响应|停止生成|停止回复)$', re.I))
            stopping = any(stop.nth(i).is_visible() for i in range(stop.count()))
            streaming = has_visible_element(page, 'article[aria-label="Currently streaming message"]')
            status = page.get_by_role('status').filter(has_text=re.compile(
                r'^(Claude is responding|Claude 正在回复|Claude 正在回覆)$', re.I))
            responding = any(status.nth(i).is_visible() for i in range(status.count()))
            if ((completion.reply_completed or current_reply_finished(page, baseline)) and not stopping and not streaming and not responding
                    and not has_visible_element(page, '[aria-busy="true"]')):
                return finish(True)
    except Error:
        return finish(False)
    return finish(False)


def execute_conversation_step(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True, help="bundles/ 下的账号名，如 claude001")
    ap.add_argument("--prompt", help="单条网页对话内容；设置后仅发送一次")
    ap.add_argument("--turns", type=int, default=1, help="发几轮")
    ap.add_argument("--probe", action="store_true", help="只看结构，不发消息")
    ap.add_argument("--my-name", default="Alex", help="账号的显示名")
    ap.add_argument("--gap", type=int, default=12, help="两轮之间的最小间隔秒数")
    ap.add_argument("--headless", action="store_true")
    args = ap.parse_args(argv)
    if args.turns <= 0:
        ap.error("--turns must be positive")
    if args.gap < 0:
        ap.error("--gap must be non-negative")

    bundle = (CFG.paths.bundles / args.account).resolve()
    if not bundle.is_dir():
        print(f"没有这个账号: {bundle}")
        return 1
    profile = bundle / "profile"
    fp_file = bundle / "fingerprint.json"
    if not profile.is_dir() or not fp_file.is_file():
        print(f"bundle 不完整（缺 profile 或 fingerprint.json）: {bundle}")
        return 1

    meta = {}
    meta_file = bundle / "meta.json"
    if meta_file.is_file():
        meta = json.loads(meta_file.read_text())

    out = CFG.paths.artifacts / f"warmup-{args.account}-{int(time.time())}"
    out.mkdir(parents=True, exist_ok=True)
    report = {"account": args.account, "email": meta.get("email"),
              "startedAt": time.strftime("%F %T"), "turns": []}

    completion = CompletionObserver()

    with Camoufox(**camoufox_kwargs(load_fingerprint(fp_file), profile,
                                    engine=meta.get("engine"),
                                    headless=args.headless)) as ctx:
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        from executor.runtime.http_trace import attach_http_trace
        attach_http_trace(page)
        page.on("request", completion.on_request)
        page.on("response", completion.on_response)
        page.on("requestfinished", completion.on_request_finished)
        page.on("requestfailed", completion.on_request_failed)
        page.set_default_timeout(45000)

        # 只导航一次。"/" 对未登录用户会 302，重复 goto 会中止正在初始化的 SPA。
        page.goto("https://claude.ai/new", wait_until="load", timeout=120000)
        # 就绪判据是 role 种类，不是标题——挑战页的标题也可能是 'Claude'
        deadline = time.time() + CFG.timing.cf_timeout
        while time.time() < deadline:
            page.wait_for_timeout(1500)
            if "verification" not in capture_accessibility_snapshot(page).lower() \
                    and len(page.evaluate("""() => {
                        const s = new Set();
                        for (const el of document.querySelectorAll('[role],button,a[href],input,textarea')) {
                          if (!(el.offsetWidth || el.offsetHeight)) continue;
                          s.add(el.getAttribute('role') || el.tagName.toLowerCase());
                        }
                        return [...s];
                    }""")) >= 2:
                break
        page.wait_for_timeout(4000)

        snap = capture_accessibility_snapshot(page)
        (out / "aria-00-landing.yaml").write_text(snap, encoding="utf-8")
        print(f"url  : {page.url}")
        print(f"title: {page.title()!r}")
        for line in snap.splitlines()[:10]:
            print(f"  {line}")

        box = find_chat_input(page)
        if box is None:
            report["stoppedAt"] = "onboarding-required" if "/onboarding" in page.url or "Let’s create your account" in snap else "missing-chat-input"
            print("\n✗ 没找到聊天输入框 —— 账号可能不在正常状态，先跑 account-retire --check")
            (out / "report.json").write_text(
                json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")
            return 1
        print("\n✓ 找到输入框")

        if args.probe:
            print("--probe：不发消息")
            (out / "report.json").write_text(
                json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")
            return 0

        # 第一轮先自然地说一句，别一上来就抛问题
        intro = f"hi, I'm {args.my_name}"
        prompts = [args.prompt] if args.prompt else [intro] + random.sample(PROMPTS, min(args.turns - 1, len(PROMPTS)))

        for i, text in enumerate(prompts, 1):
            completion.reset()
            before = capture_accessibility_snapshot(page)
            baseline = assistant_count(page)
            print(f"\n[{i}/{len(prompts)}] 发送: {text!r}")
            try:
                box = find_chat_input(page)
                if box is None:
                    print("  ✗ 输入框消失，停止")
                    report["turns"].append({"i": i, "prompt": text, "replied": False,
                                            "reason": "missing-chat-input"})
                    break
                box.fill(text)
                box.press("Enter")
            except Exception as e:
                print(f"  ✗ 发送失败: {type(e).__name__}")
                report["turns"].append({"i": i, "prompt": text, "replied": False,
                                        "reason": "send-failed"})
                break

            ok, after = wait_for_reply_completion(page, completion=completion, timeout=CFG.timing.reply_timeout, baseline=baseline)
            grew = len(after) - len(before)

            if completion.failure:
                failure = completion.failure
                report["stoppedAt"] = failure["reason"]
                report["turns"].append({"i": i, "prompt": text, "replied": False,
                                        "request_error": failure,
                                        "at": time.strftime("%T")})
                if completion.blocked_code:
                    report["blocked"] = completion.blocked_code
                print(f"  ✗ 请求失败: {failure['reason']}")
                break

            print(f"  {'✓' if ok else '⚠'} 回复{'完成' if ok else '未确认完成'}，a11y 树 +{grew} 字符")
            (out / f"aria-{i:02d}.yaml").write_text(after, encoding="utf-8")
            report["turns"].append({"i": i, "prompt": text, "replied": ok,
                                    "grew": grew, "completion": completion.evidence(),
                                    "at": time.strftime("%T")})
            if not ok:
                report["stoppedAt"] = "tool-permission-pending" if permission_pending(page) else "reply-not-confirmed"
                break
            if i < len(prompts):
                time.sleep(args.gap + random.randint(0, 8))

        # Successful replies already have the final accessibility evidence.
        # On failure capture only the viewport, never allocate a tall page image.
        if report.get("stoppedAt") or report.get("blocked"):
            try:
                page.screenshot(path=str(out / "final.png"), full_page=False, timeout=20000)
            except Exception:
                pass

        report["finalUrl"] = page.url
        report["state"] = capture_accessibility_snapshot(page)[:400]
    (out / "report.json").write_text(
        json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")
    done = sum(1 for t in report["turns"] if t["replied"])
    if report.get("blocked"):
        # 退出码 2 = 明确的"不可用"，区别于 1 = 执行出错
        print(f"\n结论: blocked:{report['blocked']}")
        print(f"产出: {out}")
        return 2
    print(f"\n完成 {done}/{len(report['turns'])} 轮")
    print(f"产出: {out}")
    return 0 if done == len(prompts) else 1


if __name__ == "__main__":
    from executor.runtime.execution_guard import execution_guard

    with execution_guard():
        raise SystemExit(execute_conversation_step())
