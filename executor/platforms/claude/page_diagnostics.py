"""页面诊断：区分明确挑战、拒绝访问和普通加载故障；不执行验证操作。"""
from dataclasses import asdict, dataclass
from urllib.parse import urlparse
import re


@dataclass(frozen=True)
class PageState:
    kind: str
    http_status: int | None = None
    human_action_required: bool = False

    def to_json(self):
        return asdict(self)


def has_visible_element(page, selector):
    loc = page.locator(selector)
    return any(loc.nth(i).is_visible() for i in range(loc.count()))


def inspect_page(page, http_status=None):
    """只读诊断。403 或文字 verification 本身不构成 CF 挑战证据。

    **'Just a moment...' 是挑战进行中，不是"需要人工"。**
    这一条以前判错了，代价很大：调用方看到 human_action_required 就立刻
    返回失败，于是配置里那 150 秒的等待窗口从来没生效过——实测全新
    profile 首轮大约 42 秒会自己放行，而它在第 0 秒就放弃了。

    真正需要人工的信号是**页面明确要求你操作**（出现可勾选的验证控件、
    或正文出现 "Verify you are human" 这类文案），不是标题里带有挑战字样。
    """
    title = page.title().strip().lower()

    # This is an application rejection after an email login link, not proof
    # of a Cloudflare challenge or a request for an interactive CAPTCHA.
    if urlparse(page.url).path.rstrip('/') == '/magic-link':
        headings = page.get_by_role('heading', level=1).all_text_contents()
        if any(h.strip().lower().replace('’', "'") == "couldn't verify your browser"
               for h in headings):
            return PageState('browser-verification-failed', http_status)

    in_progress = title in ('just a moment...', 'just a moment…')

    # 明确要人工：正文让你勾选/点击验证，或页面上有可交互的验证控件
    explicit_human = False
    try:
        body = page.locator("body").inner_text(timeout=2000).lower()
        explicit_human = any(k in body for k in (
            "verify you are human", "确认您是真人", "確認您是人類"))
    except Exception:
        pass

    challenge_container = has_visible_element(page, '#challenge-stage, #challenge-running')
    interactive = challenge_container and has_visible_element(
        page, '#challenge-stage input[type="checkbox"], '
              '#challenge-stage [role="checkbox"]')
    challenge = in_progress or challenge_container
    if challenge:
        # 进行中→继续等；明确要人工→才交给人工
        return PageState('cf-challenge', http_status,
                         human_action_required=explicit_human or interactive)

    if http_status in (401, 403):
        return PageState('access-denied', http_status)
    if http_status is not None and http_status >= 400:
        return PageState('http-error', http_status)
    path = urlparse(page.url).path
    if path.startswith('/onboarding'):
        return PageState('onboarding', http_status)
    return PageState('page', http_status)


def find_unique_editable_input(locator):
    """仅返回唯一可见且可编辑候选，歧义交给调用方处理。"""
    candidates = [locator.nth(i) for i in range(locator.count())
                  if locator.nth(i).is_visible() and locator.nth(i).is_editable()]
    return candidates[0] if len(candidates) == 1 else None


def find_chat_input(page):
    return find_unique_editable_input(page.get_by_role(
        'textbox', name=re.compile(r'write your prompt|message|prompt|消息|mensaje', re.I)))


def is_chat_page_ready(page):
    """聊天路由、无挑战、唯一明确的可编辑消息输入框缺一不可。"""
    path = urlparse(page.url).path.rstrip('/')
    if path != '/new' and path != '/chat' and not path.startswith('/chat/'):
        return False
    if inspect_page(page).kind == 'cf-challenge':
        return False
    return find_chat_input(page) is not None


def navigate_once(page, url, *, timeout=120000):
    """单次导航并记录失败类别；不重试请求，也不刷新挑战页面。"""
    from playwright.sync_api import Error, TimeoutError
    try:
        response = page.goto(url, wait_until="load", timeout=timeout)
    except TimeoutError:
        return PageState('navigation-timeout')
    except Error:
        return PageState('navigation-error')
    return inspect_page(page, response.status if response else None)
