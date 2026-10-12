import unittest
from unittest.mock import Mock
from executor.platforms.claude.page_diagnostics import inspect_page

class BrowserVerificationFailureTests(unittest.TestCase):
    def page(self, url, heading):
        page = Mock()
        page.url = url
        page.title.return_value = 'Claude'
        page.locator.return_value.count.return_value = 0
        page.locator.return_value.inner_text.return_value = heading
        page.get_by_role.return_value.all_text_contents.return_value = [heading]
        return page

    def test_magic_link_browser_rejection(self):
        for heading in ["Couldn’t verify your browser", "Couldn't verify your browser"]:
            state = inspect_page(self.page('https://claude.ai/magic-link', heading))
            self.assertEqual(state.kind, 'browser-verification-failed')
            self.assertFalse(state.human_action_required)

    def test_unrelated_page_is_not_browser_rejection(self):
        state = inspect_page(self.page('https://claude.ai/chat/fixture', "Couldn't verify your browser"))
        self.assertEqual(state.kind, 'page')

    def test_adapter_keeps_specific_reason(self):
        import json
        import tempfile
        from pathlib import Path
        from executor.platforms.claude.adapter import ClaudePlatform
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, 'report.json').write_text(json.dumps({'stoppedAt':'browser-verification-failed'}))
            self.assertEqual(ClaudePlatform()._classify_step_failure(None, directory),
                             'blocked:browser-verification-failed')
