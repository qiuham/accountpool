"""Onboarding consent action, authorized by the account-pool owner.
Uses semantic checkbox roles; skips unrelated and optional subscriptions.
"""
import re
from playwright.sync_api import Error

def check_required_onboarding_consents(page) -> int:
    """只选择明确要求的同意项，其他复选框保持不变。"""
    hit = 0
    for checkbox in page.get_by_role("checkbox", name=re.compile(
        r"I agree|I accept|同意|接受|Acepto|at least 18|年满.*18|至少.*18", re.I
    )).all():
        try:
            label = checkbox.get_attribute("aria-label") or checkbox.aria_snapshot()
            if re.search(r"marketing|promotional|newsletter|营销|推广|订阅", label, re.I):
                continue
            if not checkbox.is_visible() or not checkbox.is_enabled() or checkbox.is_checked():
                continue
            checkbox.set_checked(True)
            hit += 1
        except Error as error:
            print(f"    ! 勾选失败: {type(error).__name__}")
    if hit:
        print(f"    → 勾选 {hit} 个框")
    return hit


