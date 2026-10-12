#!/usr/bin/env python3
"""Claude 账号注册：走到要验证码那一步为止。

流程要点（都是被实际失败逼出来的，改动前先看这里）
------------------------------------------------
1. **只导航一次**。`claude.ai/` 对未登录用户会 302 到 `/login`，如果之后
   再 `goto("/login")`，第二次导航会把第一次正在初始化的 SPA 整个中止——
   页面永远停在 165 个元素的骨架、readyState 卡在 loading、aria 树为空。
   现象酷似 CF 拦截或页面改版，实际是自己把自己打断了。
2. 用 `wait_until="load"` 而不是 `"commit"`。commit 在响应头到达时就返回，
   那时 SPA 还没跑起来，任何 DOM 查询都会扑空。
3. 定位一律走无障碍树（`get_by_role` + 多语言别名），不写 CSS 选择器。
   未登录落地页是营销/定价页（Individual / Team and Enterprise + Free 套餐 +
   Try Claude），需要先找到登录入口，而不是假设直接就有邮箱表单。
4. 串行 + 最小冷却。CF 对短时间密集访问会降级，实测有必要。

    # 只看不填，验证流程能走到哪（不会发出任何请求给 Claude）
    python tools/claude-register.py --email test@hamqiu.com --probe

    # 真跑：填邮箱并点继续
    python tools/claude-register.py --name claude001
"""

from contextlib import ExitStack
import argparse
import json
import random
import re
import sys
import time
from executor.platforms.claude.page_diagnostics import find_unique_editable_input
import warnings
from pathlib import Path

warnings.filterwarnings("ignore")
from camoufox.sync_api import Camoufox  # noqa: E402
from playwright.sync_api import expect  # noqa: E402

from executor.runtime.config import CFG, PROXY, camoufox_kwargs, subset_engine  # noqa: E402
from executor.runtime.fingerprint import dump_fingerprint, load_fingerprint  # noqa: E402
from executor.platforms.claude.page_diagnostics import inspect_page, navigate_once, is_chat_page_ready
from executor.runtime.browser_services import BrowserServices, BrowserServiceError

# 登录入口的别名。落地页上可能是 "Try Claude" / "Sign in" / "Log in"，
# 也可能直接就是邮箱表单。全部锚定，避免把 "Try Claude Pro" 之类当成入口。
LOGIN_INTENT = r"^\s*(Sign in|Log in|Login|Try Claude|Get started|Continue with email|Iniciar sesi[oó]n|Probar Claude)\s*$"
EMAIL_INTENT = r"^\s*(Continue|Continuar|Next|Siguiente|Continue with email|Submit)\s*$"



def open_login_link(page, ctx, link: str) -> str:
    """Navigate once; a missing load event does not prove login failed."""
    from playwright.sync_api import Error, TimeoutError
    timed_out = False
    try:
        try:
            page.goto(link, wait_until="domcontentloaded", timeout=120000)
        except TimeoutError:
            timed_out = True
        for attempt in range(21):
            if any(c["name"] == "sessionKey" and c.get("value") for c in ctx.cookies()):
                return ""
            if attempt < 20:
                page.wait_for_timeout(3000)
        return "login-navigation-timeout" if timed_out else "login-session-not-established"
    except Error:
        return "login-navigation-error"


def capture_accessibility_snapshot(page) -> str:
    try:
        return page.locator("body").aria_snapshot()
    except Exception as e:
        return f"<aria 不可用: {type(e).__name__}>"


def count_visible_roles(page) -> dict:
    """可见元素的 role 分布，用来判断「这是哪一页」。"""
    try:
        return page.evaluate("""() => {
          const o = {};
          for (const el of document.querySelectorAll('[role],button,a[href],input,textarea,[contenteditable="true"]')) {
            if (!(el.offsetWidth || el.offsetHeight)) continue;
            const r = (el.getAttribute('role') || el.tagName.toLowerCase()).toLowerCase();
            if (r === 'presentation' || r === 'none') continue;
            o[r] = (o[r] || 0) + 1;
          }
          return o;
        }""")
    except Exception:
        return {}


def wait_for_page_readiness(page, timeout_s: int | None = None,
                            require_email: bool = True) -> bool:
    """等待可填写邮箱，或已登录且聊天输入框就绪。

    **判据必须是具体目标，不能是"页面上有可交互元素"。**
    这一条以前写宽了，代价是：claude.ai 过完挑战后有个中间态
    （标题从 'Just a moment...' 变成 'Claude'），那个页面上按钮和链接
    一应俱全，但**邮箱框要再等约 50 秒才出现**。宽判据会在邮箱框出现前
    就返回"就绪"，接着流程去找邮箱框、找不到、报 spa-not-rendered。

    实测时间线（全新 profile，humanize=True）：
        0s   Just a moment...
        30s  Claude          ← 中间态，宽判据会在这一刻误报就绪
        81s  Sign in - Claude ← 真正就绪，邮箱框出现
    """
    timeout_s = timeout_s if timeout_s is not None else CFG.timing.cf_timeout
    deadline = time.monotonic() + timeout_s
    last_kind = None
    while time.monotonic() < deadline:
        state = inspect_page(page)
        if state.human_action_required or state.kind == "access-denied":
            return False
        last_kind = state.kind

        if require_email:
            if is_chat_page_ready(page) or find_email_input(page) is not None:
                return True
        else:
            # 不要求邮箱框时（例如已登录态做别的动作），才放宽到"有可交互元素"
            controls = page.locator('input, textarea, button, a[href], '
                                    '[role="button"], [role="textbox"], '
                                    '[contenteditable="true"]')
            for i in range(controls.count()):
                control = controls.nth(i)
                if control.is_visible() and control.is_enabled():
                    return True

        page.wait_for_timeout(min(500, max(0, (deadline - time.monotonic()) * 1000)))
    print(f"    [等待超时] 最后页面状态: {last_kind}  url={page.url[:80]}")
    return False


def find_email_input(page):
    """只选唯一明确的邮箱候选，搜索框或多个邮箱框不做猜测。"""
    name = re.compile(r"email|e-mail|correo|邮箱|电子邮件", re.I)
    candidates = page.get_by_label(name).or_(page.get_by_role("textbox", name=name)).or_(
        page.locator('input[type="email"], input[autocomplete="email"]'))
    return find_unique_editable_input(candidates)


def click_login_action(page, pattern: str) -> bool:
    """按可访问名称点按钮/链接。名称默认子串匹配，正则里自行锚定。"""
    for role in ("button", "link"):
        loc = page.get_by_role(role, name=re.compile(pattern, re.I))
        attempted = False
        try:
            for i in range(loc.count()):
                el = loc.nth(i)
                if el.is_visible():
                    expect(el).to_be_enabled(timeout=8000)
                    attempted = True
                    el.click()
                    print(f"    → 点击 [{role}]")
                    return True
        except Exception:
            if attempted:
                raise
            continue
    return False


def save_page_snapshot(page, out: Path, name: str) -> str:
    snap = capture_accessibility_snapshot(page)
    (out / f"{name}.yaml").write_text(snap, encoding="utf-8")
    try:
        (out / f"{name}.html").write_text(page.content(), encoding="utf-8")
    except Exception:
        pass
    return snap


def execute_registration_step(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", help="账号名，决定 bundle 目录（服务端分配）")
    ap.add_argument("--email", help="服务端分配的邮箱")
    ap.add_argument("--profile", help="profile 目录；默认按账号名")
    ap.add_argument("--fingerprint", help="复用已有指纹；不给则新生成")
    ap.add_argument("--executable-path", help="指定引擎；不给则按账号分配字体子集")
    ap.add_argument("--probe", action="store_true",
                    help="只走到找到邮箱输入框为止，不填、不提交")
    ap.add_argument("--headless", action="store_true")
    args = ap.parse_args(argv)

    # --- 账号名与邮箱 ---
    # 两个都由服务端给。执行侧不自己造账号名、不自己建邮箱——
    # 账号名决定 bundle 目录和墓碑归属，邮箱是物料，都是号池级的决策。
    email = args.email
    account = args.account
    if not account:
        print("缺少账号名（--account）：账号名由服务端分配，执行侧不自己编号")
        return 1
    # 邮箱由服务端给，执行侧不自己建。
    #
    # 为什么：邮箱是**物料**，归号池管——要防撞名、要记墓碑、要按域名算风险分、
    # 要在报废时删掉。执行侧只该消费服务端分配好的物料，不该做这些决策。
    # 所以这里没有邮箱就直接报错，不 fallback 到"自己建一个"。
    if not email:
        print("缺少邮箱：邮箱由服务端分配并通过 spec 传入，执行侧不自己建")
        print("  （服务端应先调 accountpool run <name> --steps register 建好，再把地址和 jwt 传进来）")
        return 1
    print(f"使用服务端分配的邮箱: {email}")

    # --- bundle / profile / 指纹 / 引擎 ---
    bundle = CFG.paths.bundles / account
    bundle.mkdir(parents=True, exist_ok=True)
    profile = Path(args.profile).resolve() if args.profile else bundle / "profile"
    profile.mkdir(parents=True, exist_ok=True)

    fp_file = bundle / "fingerprint.json"
    if args.fingerprint:
        fp = load_fingerprint(Path(args.fingerprint))
    elif fp_file.is_file():
        fp = load_fingerprint(fp_file)
    else:
        from browserforge.fingerprints import FingerprintGenerator
        fp = FingerprintGenerator(browser="firefox", os=CFG.browser.os).generate()
    dump_fingerprint(fp, fp_file)

    engine = args.executable_path or subset_engine(account)
    meta = {
        "name": account, "email": email, "engine": engine,
        "fingerprint": str(fp_file), "createdAt": time.strftime("%F %T"),
    }
    (bundle / "meta.json").write_text(
        json.dumps(meta, indent=2, ensure_ascii=False), encoding="utf-8")

    out = CFG.paths.artifacts / f"register-{account}-{int(time.time())}"
    out.mkdir(parents=True, exist_ok=True)
    report = {"account": account, "email": email, "engine": engine,
              "profile": str(profile), "probe": args.probe,
              "startedAt": time.strftime("%F %T")}

    print(f"账号   : {account}")
    print(f"邮箱   : {email}")
    print(f"profile: {profile}")
    print(f"引擎   : {engine or '(默认)'}")

    # profile 与引擎版本必须配套，降级会卡在兼容性迁移上
    compat = profile / "compatibility.ini"
    if compat.is_file():
        for line in compat.read_text(errors="replace").splitlines():
            if line.startswith("LastVersion="):
                print(f"注意   : 该 profile 上次由 {line.split('=',1)[1].split('_')[0]} 使用")

    with ExitStack() as resources, Camoufox(**camoufox_kwargs(fp, profile, engine=engine, headless=args.headless)) as ctx:
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        from executor.runtime.http_trace import attach_http_trace
        attach_http_trace(page)
        page.set_default_timeout(45000)

        # --- 1) 唯一的一次导航 ---
        print("\n[1] 打开 claude.ai（只导航这一次）")
        state = navigate_once(page, "https://claude.ai/")
        status = state.http_status
        report["pageState"] = state.to_json()

        # **CF 挑战是过程，不是终点。**
        #
        # 这里以前把 cf-challenge 也归进"立即停"的分支，结果是：navigate_once
        # 用 wait_until="load" 返回时挑战必然还在进行，于是每次都在第 0 秒退出，
        # 下面那个 wait_for_page_readiness（配置里给了 150 秒）**从来没被调用过**。
        # 实测它其实会自己放行——全新 profile 大约 42–87 秒，见 wait_for_page_readiness
        # 的注释里的时间线。
        #
        # 所以分两类处理：
        #   导航本身失败 / 被拒绝       → 这里就停，等下去也没用
        #   明确要人工（出现验证控件）   → 停，交给人
        #   挑战进行中（cf-challenge）  → 往下走，交给等待逻辑
        if state.kind in ("access-denied", "http-error",
                          "navigation-timeout", "navigation-error"):
            report["stoppedAt"] = state.kind
            (out / "report.json").write_text(
                json.dumps(report, indent=2, ensure_ascii=False))
            print("页面诊断:", state.kind, "（导航层面就失败，不重试）")
            return 1

        if state.human_action_required:
            report["stoppedAt"] = "cf-challenge"
            (out / "report.json").write_text(
                json.dumps(report, indent=2, ensure_ascii=False))
            print("页面诊断: cf-challenge，需要人工验证后再继续")
            return 2

        if state.kind == "cf-challenge":
            print("页面诊断: cf-challenge（进行中，等它自动放行）")

        ok = wait_for_page_readiness(page)
        snap = save_page_snapshot(page, out, "1-landing")
        print(f"    url={page.url}  settled={ok}")
        print(f"    role={count_visible_roles(page)}")
        for line in snap.splitlines()[:8]:
            print(f"      {line}")
        if not ok:
            print("    ✗ SPA 没渲染出内容，看 1-landing.html")
            state = inspect_page(page, status)
            report["pageState"] = state.to_json()
            report["stoppedAt"] = "cf-challenge" if state.human_action_required else "spa-not-rendered"
            (out / "report.json").write_text(json.dumps(report, indent=2, ensure_ascii=False))
            return 1

        # --- 2) 找邮箱输入框；没有就先找登录入口 ---
        if is_chat_page_ready(page) and any(
                cookie.get("name") == "sessionKey" and cookie.get("value")
                for cookie in ctx.cookies("https://claude.ai")):
            report.update({"form": "existing-session", "finalUrl": page.url,
                           "sessionKey": True})
            (out / "report.json").write_text(
                json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")
            print("已有会话且聊天页面就绪，无需重复提交邮箱")
            return 0
        print("\n[2] 找邮箱输入框")
        box = find_email_input(page)
        if box is None:
            print("    落地页没有邮箱表单，尝试找登录入口")
            if click_login_action(page, LOGIN_INTENT):
                page.wait_for_timeout(5000)
                save_page_snapshot(page, out, "2-after-login-click")
                print(f"    url={page.url}  role={count_visible_roles(page)}")
                box = find_email_input(page)

        if box is None:
            snap = save_page_snapshot(page, out, "2-no-email-box")
            report["stoppedAt"] = "email-box-missing"
            (out / "report.json").write_text(json.dumps(report, indent=2, ensure_ascii=False))
            print("    ✗ 没找到邮箱输入框。当前页面结构：")
            for line in snap.splitlines()[:14]:
                print(f"      {line}")
            print(f"    产出: {out}")
            return 1

        print("    ✓ 找到邮箱输入框")
        report["emailBoxFound"] = True
        if args.probe:
            print("\n--probe：到此为止，不填不提交")
            (out / "report.json").write_text(json.dumps(report, indent=2, ensure_ascii=False))
            print(f"产出: {out}")
            return 0

        # --- 3) 填邮箱 ---
        print(f"\n[3] 填邮箱 {email}")
        # 用元素级输入，不用 page.keyboard.type()。后者是页面级键盘事件，
        # 依赖焦点恰好落在输入框上；焦点一旦被覆盖层或 React 受控组件吃掉，
        # 字会静默打在空气里，直到提交才报 "Email address is required"。
        # Hydration may replace/reset the input after it first becomes visible.
        # Repeat only local filling, never the submission or navigation.
        for attempt in range(3):
            box.fill(email)
            page.wait_for_timeout(1000)
            if box.input_value() == email:
                break
        page.wait_for_timeout(random.randint(500, 1000))
        snap = save_page_snapshot(page, out, "3-filled")
        # 填完先自查一次，别等提交才发现没输进去
        try:
            got = (box.input_value() or "").strip()
            print(f"    输入框当前值: {got!r}")
            if got != email:
                print(f"    ✗ 值不对（期望 {email!r}），中止")
                report["stoppedAt"] = "email-not-entered"
                (out / "report.json").write_text(
                    json.dumps(report, indent=2, ensure_ascii=False))
                return 1
        except Exception as e:
            print(f"    (读值失败: {type(e).__name__})")

        # --- 4) 提交 ---
        try:
            mail_service = resources.enter_context(BrowserServices())
            mail_service.call("mail.snapshot")
        except BrowserServiceError as error:
            report['stoppedAt'] = error.reason
            (out / 'report.json').write_text(json.dumps(report, indent=2))
            return 1
        print("\n[4] 提交")
        if not click_login_action(page, EMAIL_INTENT):
            page.keyboard.press("Enter")   # 有些形态下回车即可提交
            print("    → 回车提交")
        page.wait_for_timeout(6000)
        snap = save_page_snapshot(page, out, "4-after-submit")
        print(f"    url={page.url}  role={count_visible_roles(page)}")
        for line in snap.splitlines()[:10]:
            print(f"      {line}")

        state = inspect_page(page)
        if state.human_action_required:
            report.update({"stoppedAt": "cf-challenge", "pageState": state.to_json()})
            (out / "report.json").write_text(json.dumps(report, indent=2, ensure_ascii=False))
            return 2

        # Go owns inbox polling and proof parsing while this page stays alive.
        print(f"\n[5] 等待 Go 返回登录凭证（最多 {CFG.timing.code_timeout}s）")
        try:
            proof = mail_service.call("mail.wait", timeout=CFG.timing.code_timeout)["proof"]
        except BrowserServiceError as error:
            report["stoppedAt"] = error.reason
            (out / "report.json").write_text(json.dumps(report, indent=2))
            return 1
        links = [proof["link"]] if proof.get("link") else []
        code = proof.get("code")

        if links:
            print("\n[6] 打开本次登录链接")
            report["form"] = "magic-link"
            # 必须在本 profile 内打开：cf_clearance 和一次性 token 都绑在这个会话上
            failure = open_login_link(page, ctx, links[0])
            if failure:
                report["stoppedAt"] = failure
                # Keep failure-page evidence; never persist the one-time link.
                try:
                    (out / "login-failure.yaml").write_text(capture_accessibility_snapshot(page), encoding="utf-8")
                    page.screenshot(path=str(out / "login-failure.png"), full_page=True, timeout=15000)
                except Exception as error:
                    report["diagnosticError"] = type(error).__name__
                # Browser closure and navigation errors must still produce a report.
                try:
                    final_state = inspect_page(page)
                    report["pageState"] = final_state.to_json()
                    if final_state.kind not in ("page", "spa-not-rendered"):
                        report["stoppedAt"] = final_state.kind
                except Exception:
                    pass
                (out / "report.json").write_text(
                    json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")
                return 1
        elif code:
            print("\n[6] 填写本次验证码")
            report["form"] = "code"
            box = find_email_input(page) or page.get_by_role("textbox").first
            if box is not None:
                box.click()
                box.press_sequentially(code, delay=random.randint(30, 80))
                page.wait_for_timeout(500)
                if not click_login_action(page, EMAIL_INTENT):
                    page.keyboard.press("Enter")
                for _ in range(20):
                    page.wait_for_timeout(3000)
                    if "sessionKey" in {c["name"] for c in ctx.cookies()}:
                        break
        else:
            report["form"] = "unknown"
            print("\n[6] 邮件里既没找到链接也没找到 6 位码，检查 Go 邮件服务结果")

        save_page_snapshot(page, out, "6-final")
        names = sorted({c["name"] for c in ctx.cookies()})
        got = "sessionKey" in names
        print(f"\n    url={page.url}")
        print(f"    cookies={names}")
        print(f"    {'✅ 拿到 sessionKey，注册成功' if got else '⚠ 没有 sessionKey'}")

        if not got:
            final_state = inspect_page(page)
            report["pageState"] = final_state.to_json()
            report["stoppedAt"] = (final_state.kind if final_state.kind != "page"
                                   else "login-session-not-established")

        report.update({"form": report.get("form"), "finalUrl": page.url,
                       "cookies": names, "sessionKey": got})
        (out / "report.json").write_text(json.dumps(report, indent=2, ensure_ascii=False),
                                         encoding="utf-8")
        print(f"\n产出: {out}")
        return 0 if got else 1


if __name__ == "__main__":
    from executor.runtime.execution_guard import execution_guard

    with execution_guard():
        raise SystemExit(execute_registration_step())
