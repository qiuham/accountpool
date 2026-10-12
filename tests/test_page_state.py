import os
import unittest
from pathlib import Path
from browser_fixture import CamoufoxBrowserTests
from executor.platforms.claude.page_diagnostics import inspect_page, is_chat_page_ready, navigate_once


class LocalBrowserTests(CamoufoxBrowserTests):


    def setUp(self):
        super().setUp()
        self.context = self.browser.new_context(offline=True)
        self.context.route('**/*', lambda route: route.abort())
        self.addCleanup(self.context.close)
        self.page = self.context.new_page()

    def test_real_challenge_requires_human(self):
        self.page.set_content('<title>Just a moment...</title><div id="challenge-stage">Verify you are human</div>')
        result = inspect_page(self.page, 403)
        self.assertEqual(result.kind, 'cf-challenge')
        self.assertTrue(result.human_action_required)

    def test_automatic_challenge_is_not_human_prompt(self):
        self.page.set_content('<title>Just a moment...</title><div id="challenge-stage">Checking if the site connection is secure</div>')
        state = inspect_page(self.page, 403)
        self.assertEqual(state.kind, 'cf-challenge')
        self.assertFalse(state.human_action_required)

    def test_checkbox_is_manual_even_with_generic_text(self):
        self.page.set_content('<title>Just a moment...</title><div id="challenge-stage"><input type="checkbox">Verify</div>')
        self.assertTrue(inspect_page(self.page).human_action_required)

    def test_generic_verify_text_does_not_prove_manual_action(self):
        self.page.set_content('<title>Just a moment...</title><div id="challenge-stage">Verify</div>')
        self.assertFalse(inspect_page(self.page).human_action_required)

    def test_unrelated_human_text_is_not_cf(self):
        self.page.set_content('<title>Help</title><p>Verify you are human</p>')
        self.assertEqual(inspect_page(self.page).kind, 'page')

    def test_login_verification_text_is_not_cloudflare(self):
        self.page.set_content('<title>Login</title><label>Verification code<input></label>')
        self.assertEqual(inspect_page(self.page, 200).kind, 'page')

    def test_403_without_challenge_is_access_denied(self):
        self.page.set_content('<title>Access denied</title>Forbidden')
        result = inspect_page(self.page, 403)
        self.assertEqual(result.kind, 'access-denied')
        self.assertFalse(result.human_action_required)

    def test_load_error_is_not_cloudflare(self):
        self.page.set_content('<title>Unavailable</title>')
        self.assertEqual(inspect_page(self.page, 503).kind, 'http-error')

    def test_failed_navigation_is_not_a_cloudflare_verdict(self):
        result = navigate_once(self.page, 'https://fixture.invalid/failed', timeout=1000)
        self.assertEqual(result.kind, 'navigation-error')
        self.assertFalse(result.human_action_required)

    def test_login_box_does_not_finish_onboarding(self):
        self.page.set_content('<title>Login</title><input aria-label="Email">')
        self.assertFalse(is_chat_page_ready(self.page))

    def test_chat_requires_editable_box(self):
        # 本地路由直接提供固定 HTML，网络层没有上游请求。
        self.context.set_offline(False)
        self.context.unroute('**/*')
        self.context.route('**/*', lambda route: route.fulfill(
            status=200, content_type='text/html', body='<title>Chat</title><textarea aria-label="Message"></textarea>'))
        self.page.goto('http://fixture.invalid/new')
        self.assertTrue(is_chat_page_ready(self.page))
        self.page.locator('textarea').evaluate('(el) => el.readOnly = true')
        self.assertFalse(is_chat_page_ready(self.page))


if __name__ == '__main__':
    unittest.main()
