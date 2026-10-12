"""执行真实 onboarding 入口，但页面全部由本地路由提供，无上游请求。"""
import contextlib
import io
import json
import os
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
from browser_fixture import CamoufoxBrowserTests
from executor.platforms.claude import onboarding


class OnboardingEntryTests(CamoufoxBrowserTests):


    def run_fixture(self, path, html, *, probe=True):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bundle = root / 'bundles' / 'fixture'
            (bundle / 'profile').mkdir(parents=True)
            (bundle / 'fingerprint.json').write_text('{}')
            cfg = SimpleNamespace(paths=SimpleNamespace(bundles=root / 'bundles', artifacts=root / 'artifacts'),
                                  identity=SimpleNamespace(display_name='Fixture'),
                                  timing=SimpleNamespace(cf_timeout=1, reply_timeout=2))
            context = self.browser.new_context(offline=True)
            page = context.new_page()
            page.wait_for_timeout = lambda _: None
            network_hits = []

            def route_fixture(route):
                network_hits.append(route.request.url)
                route.fulfill(status=200, content_type='text/html', body=html)
            context.route('**/*', route_fixture)
            page.goto('https://fixture.invalid' + path)

            @contextlib.contextmanager
            def browser_context(**kwargs):
                try:
                    yield context
                finally:
                    context.close()

            with patch.object(onboarding, 'CFG', cfg), \
                 patch.object(onboarding, 'Camoufox', browser_context), \
                 patch.object(onboarding, 'load_fingerprint', lambda _: {}), \
                 patch.object(onboarding, 'camoufox_kwargs', lambda *a, **kw: {}), \
                 contextlib.redirect_stdout(io.StringIO()):
                rc = onboarding.execute_onboarding_step(['--account', 'fixture', '--max-steps', '5'] + (['--probe'] if probe else []))
            reports = list((root / 'artifacts').glob('*/report.json'))
            self.assertEqual(len(reports), 1)
            self.assertTrue(network_hits)  # 所有导航都被上述 route.fulfill 截获。
            return rc, json.loads(reports[0].read_text())

    def test_login_page_is_not_success(self):
        rc, report = self.run_fixture('/login', '<title>Login</title><input aria-label="Email"><button>Continue</button>')
        self.assertEqual(rc, 1)
        self.assertFalse(report['completed'])
        self.assertEqual(report['stoppedAt'], 'unexpected-page')

    def test_unfinished_onboarding_is_not_success(self):
        rc, report = self.run_fixture('/onboarding', '<title>Welcome</title><input aria-label="Name"><button>Continue</button>')
        self.assertEqual(rc, 1)
        self.assertFalse(report['completed'])
        self.assertEqual(report['stoppedAt'], 'onboarding-incomplete')

    def test_challenge_stops_before_form_interaction(self):
        rc, report = self.run_fixture('/new', '<title>Just a moment...</title><div id="challenge-stage">Verify you are human</div>')
        self.assertEqual(rc, 2)
        self.assertEqual(report['stoppedAt'], 'cf-challenge')
        self.assertTrue(report['pageState']['human_action_required'])
        self.assertEqual(report['steps'], [])

    def test_actual_chat_page_is_success(self):
        rc, report = self.run_fixture('/new', '<title>Chat</title><textarea aria-label="Write your prompt to Claude"></textarea><button>Send</button>')
        self.assertEqual(rc, 0)
        self.assertTrue(report['completed'])

    def test_authorized_consent_completes_initialization(self):
        html = """<title>Welcome</title>
        <input id="terms" type="checkbox" aria-label="I agree to the Consumer Terms and confirm that I am at least 18 years of age." onchange="document.querySelector('button').disabled=!this.checked">
        <button disabled onclick="history.pushState({},'', '/new');document.body.innerHTML='<textarea aria-label=&quot;Write your prompt to Claude&quot;></textarea><button>Send</button>'">Continue</button>"""
        rc, report = self.run_fixture('/onboarding', html, probe=False)
        self.assertEqual(rc, 0)
        self.assertTrue(report['completed'])
        self.assertIn('check', [step.get('action') for step in report['steps']])


if __name__ == '__main__':
    unittest.main()
