import contextlib
import io
import json
import os
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch, Mock
from browser_fixture import CamoufoxBrowserTests
from executor.platforms.claude import conversation

class ConversationResultTests(CamoufoxBrowserTests):


    def test_stable_error_banner_is_not_reply(self):
        context = self.browser.new_context(offline=True)
        self.addCleanup(context.close)
        page = context.new_page()
        page.wait_for_timeout = lambda _: None
        page.set_content('<div role="alert">'+ 'Request failed. ' * 12 + '</div>')
        ok, _ = conversation.wait_for_reply_completion(page, timeout=0.2, completion=conversation.CompletionObserver())
        self.assertFalse(ok)

    def test_stable_streaming_page_is_not_finished(self):
        context = self.browser.new_context(offline=True)
        self.addCleanup(context.close)
        page = context.new_page()
        page.wait_for_timeout = lambda _: None
        page.set_content('<div aria-busy="true">' + 'Still generating. ' * 12 + '</div>')
        ok, _ = conversation.wait_for_reply_completion(page, timeout=0.2, completion=conversation.CompletionObserver())
        self.assertFalse(ok)

    def run_fixture(self, reply_ok, turns=1, forbid_global_keyboard=False):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bundle = root / 'bundles' / 'fixture'
            (bundle / 'profile').mkdir(parents=True)
            (bundle / 'fingerprint.json').write_text('{}')
            cfg = SimpleNamespace(paths=SimpleNamespace(bundles=root/'bundles', artifacts=root/'artifacts'), timing=SimpleNamespace(cf_timeout=1, reply_timeout=2))
            context = self.browser.new_context()
            context.route('**/*', lambda route: route.fulfill(status=200, content_type='text/html', body='<textarea aria-label="Message"></textarea><button>Send</button>'))
            page = context.new_page()
            page.wait_for_timeout = lambda _: None
            if forbid_global_keyboard:
                page.keyboard.type = Mock(side_effect=AssertionError('global keyboard typing'))
                page.keyboard.press = Mock(side_effect=AssertionError('global keyboard press'))
            @contextlib.contextmanager
            def local_browser(**kwargs):
                try:
                    yield context
                finally:
                    context.close()
            with contextlib.chdir(root), patch.object(conversation, 'CFG', cfg), patch.object(conversation, 'Camoufox', local_browser), patch.object(conversation, 'load_fingerprint', return_value={}), patch.object(conversation, 'camoufox_kwargs', return_value={}), patch.object(conversation, 'wait_for_reply_completion', return_value=(reply_ok, 'fixture snapshot')), contextlib.redirect_stdout(io.StringIO()):
                rc = conversation.execute_conversation_step(['--account','fixture','--turns',str(turns),'--gap','0'])
            files = list((root/'artifacts').glob('*/report.json'))
            self.assertEqual(len(files), 1)
            return rc, json.loads(files[0].read_text())

    def test_failed_reply_returns_nonzero_and_stops(self):
        rc, report = self.run_fixture(False, turns=2)
        self.assertEqual(rc, 1)
        self.assertEqual(len(report['turns']), 1)
        self.assertFalse(report['turns'][0]['replied'])

    def test_success_report_is_captured_before_browser_closes(self):
        rc, report = self.run_fixture(True)
        self.assertEqual(rc, 0)
        self.assertEqual(report['finalUrl'], 'https://claude.ai/new')
        self.assertNotIn('不可用', report['state'])

    def test_nonpositive_turns_rejected(self):
        with self.assertRaises(SystemExit) as raised:
            conversation.execute_conversation_step(['--account','fixture','--turns','0'])
        self.assertEqual(raised.exception.code, 2)

    def test_snapshot_failure_is_not_reply(self):
        context = self.browser.new_context(offline=True)
        self.addCleanup(context.close)
        page = context.new_page()
        page.wait_for_timeout = lambda _: None
        with patch.object(conversation, 'capture_accessibility_snapshot', return_value='<aria 不可用: TargetClosedError>'):
            ok, _ = conversation.wait_for_reply_completion(page, timeout=0.2, completion=conversation.CompletionObserver())
        self.assertFalse(ok)

    def test_negative_gap_rejected(self):
        with self.assertRaises(SystemExit) as raised:
            conversation.execute_conversation_step(['--account','fixture','--gap','-1'])
        self.assertEqual(raised.exception.code, 2)

    def test_success_does_not_stop_remaining_rounds(self):
        rc, report = self.run_fixture(True, turns=2)
        self.assertEqual(rc, 0)
        self.assertEqual(len(report['turns']), 2)
        self.assertTrue(all(turn['replied'] for turn in report['turns']))

    def test_element_scoped_submission_avoids_global_keyboard(self):
        rc, report = self.run_fixture(True, forbid_global_keyboard=True)
        self.assertEqual(rc, 0)
        self.assertTrue(report['turns'][0]['replied'])
