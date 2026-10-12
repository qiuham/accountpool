"""Current-turn UI completion and one-time public web-read permissions."""
from playwright.sync_api import Error
import ipaddress
import re
from urllib.parse import urlparse

def assistant_count(page):
    return page.get_by_role("heading", name=re.compile(r"^Claude responded:", re.I)).count()

def permission_pending(page):
    return page.get_by_role("group", name=re.compile(r"^Permission request:", re.I)).count() > 0

def allow_web_read_once(page, handled):
    groups = page.get_by_role("group", name="Permission request: Web fetch", exact=True)
    if groups.count() != 1 or not groups.is_visible():
        return False
    urls = groups.get_by_role("definition")
    if urls.count() != 1:
        return False
    raw = urls.inner_text().strip()
    u = urlparse(raw)
    host = (u.hostname or "").lower()
    if u.scheme not in ("http", "https") or u.username or u.password or not host or "." not in host:
        return False
    if host.endswith((".local", ".localhost", ".internal")):
        return False
    try:
        if not ipaddress.ip_address(host).is_global:
            return False
    except ValueError:
        pass
    if raw in handled or len(handled) >= 3:
        return False
    button = groups.get_by_role("button", name="Allow once", exact=True)
    if button.count() != 1 or not button.is_enabled():
        return False
    handled.add(raw)  # Never repeat an ambiguous click.
    try:
        button.click(timeout=5000)
    except Error:
        return False
    return True

def current_reply_finished(page, baseline):
    if baseline is None or assistant_count(page) <= baseline or permission_pending(page):
        return False
    articles = page.get_by_role("article").filter(
        has=page.get_by_role("heading", name=re.compile(r"^Claude responded:", re.I)))
    if not articles.count():
        return False
    last = articles.last
    finished = page.get_by_role("status").filter(
        has_text=re.compile(r"^Claude finished the response$", re.I))
    return (finished.count() > 0
            and last.get_by_role("button", name="Copy", exact=True).count() == 1
            and last.get_by_role("button", name="Good response", exact=True).count() == 1)
