import unittest
from unittest.mock import Mock
from playwright.sync_api import TimeoutError, Error
from executor.platforms.claude.registration import open_login_link

class LoginLinkTests(unittest.TestCase):
    def test_timeout_with_session_succeeds_without_replay(self):
        page, ctx = Mock(), Mock()
        page.goto.side_effect = TimeoutError("fixture")
        ctx.cookies.return_value = [{"name": "sessionKey", "value": "fixture"}]
        self.assertEqual(open_login_link(page, ctx, "https://fixture.invalid"), "")
        page.goto.assert_called_once()

    def test_timeout_without_session_has_specific_reason(self):
        page, ctx = Mock(), Mock()
        page.goto.side_effect = TimeoutError("fixture")
        ctx.cookies.return_value = []
        self.assertEqual(open_login_link(page, ctx, "https://fixture.invalid"), "login-navigation-timeout")
        page.goto.assert_called_once()

    def test_closed_browser_is_reportable(self):
        page, ctx = Mock(), Mock()
        page.goto.side_effect = Error("browser closed")
        self.assertEqual(open_login_link(page, ctx, "https://fixture.invalid"), "login-navigation-error")

    def test_empty_session_is_not_success(self):
        page, ctx = Mock(), Mock()
        ctx.cookies.return_value = [{"name": "sessionKey", "value": ""}]
        self.assertEqual(open_login_link(page, ctx, "https://fixture.invalid"), "login-session-not-established")
