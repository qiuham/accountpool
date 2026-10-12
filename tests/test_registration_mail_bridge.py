"""Real browser registration against routed local pages and a private IPC fixture."""
import contextlib
import io
import json
import os
import socket
import tempfile
import threading
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
from browser_fixture import CamoufoxBrowserTests
from executor.platforms.claude import registration


class RegistrationMailBridgeTests(CamoufoxBrowserTests):
    def run_flow(self, failure=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            bundle = root / 'bundles/fixture'
            (bundle / 'profile').mkdir(parents=True)
            (bundle / 'fingerprint.json').write_text('{}')
            cfg = SimpleNamespace(paths=SimpleNamespace(bundles=root / 'bundles', artifacts=root / 'artifacts'),
                                  timing=SimpleNamespace(cf_timeout=2, code_timeout=1),
                                  browser=SimpleNamespace(os='windows'))
            context = self.browser.new_context(offline=True)
            page = context.new_page()
            page.wait_for_timeout = lambda _: None
            html = '<title>Claude</title><input type="email" aria-label="Email"><button onclick="document.body.dataset.submits=String(Number(document.body.dataset.submits || 0)+1)">Continue with email</button>'
            def route_page(route):
                if '/magic-link' in route.request.url:
                    context.add_cookies([{'name': 'sessionKey', 'value': 'fixture-session', 'url': 'https://claude.ai'}])
                route.fulfill(status=200, content_type='text/html', body=html)
            context.route('**/*', route_page)
            parent, child = socket.socketpair()
            self.addCleanup(parent.close)
            self.addCleanup(child.close)
            requests = []
            def service():
                with parent.makefile('rwb') as stream:
                    for line in stream:
                        request = json.loads(line)
                        requests.append(request['method'])
                        response = {'ok': True}
                        if request['method'] == 'mail.snapshot' and failure == 'credential':
                            response = {'error': 'mailbox-credential-invalid'}
                        if request['method'] == 'mail.wait':
                            response = ({'error': 'mail-timeout'} if failure == 'timeout' else
                                        {'ok': True, 'proof': {'link': 'https://claude.ai/magic-link#fixture'}})
                        stream.write(json.dumps(response).encode() + b'\n')
                        stream.flush()
                        if response.get('error') or request['method'] == 'mail.wait':
                            break
            thread = threading.Thread(target=service, daemon=True)
            thread.start()
            @contextlib.contextmanager
            def browser_context(**kwargs):
                try:
                    yield context
                finally:
                    context.close()
            output = io.StringIO()
            with patch.dict(os.environ, {'ACCOUNTPOOL_RPC_FD': str(child.fileno())}), \
                 patch.object(registration, 'CFG', cfg), \
                 patch.object(registration, 'Camoufox', browser_context), \
                 patch.object(registration, 'load_fingerprint', lambda _: {}), \
                 patch.object(registration, 'dump_fingerprint', lambda *a: None), \
                 patch.object(registration, 'subset_engine', lambda _: None), \
                 patch.object(registration, 'camoufox_kwargs', lambda *a, **kw: {}), \
                 contextlib.redirect_stdout(output):
                status = registration.execute_registration_step(['--account', 'fixture', '--email', 'fixture@example.test'])
            child.close()
            thread.join(timeout=2)
            self.assertFalse(thread.is_alive())
            report = json.loads(next((root / 'artifacts').glob('*/report.json')).read_text())
            self.assertFalse(list((root / 'artifacts').glob('*/mail.json')))
            snapshot = list((root / 'artifacts').glob('*/4-after-submit.html'))
            submissions = 1 if snapshot and 'data-submits="1"' in snapshot[0].read_text() else 0
            return status, report, requests, submissions

    def test_login_keeps_browser_alive_while_go_provides_proof(self):
        status, report, requests, submissions = self.run_flow()
        self.assertEqual(status, 0)
        self.assertTrue(report['sessionKey'])
        self.assertEqual(requests, ['mail.snapshot', 'mail.wait'])
        self.assertEqual(submissions, 1)

    def test_mailbox_failure_stops_before_submission(self):
        status, report, requests, submissions = self.run_flow('credential')
        self.assertEqual(status, 1)
        self.assertEqual(report['stoppedAt'], 'mailbox-credential-invalid')
        self.assertEqual(requests, ['mail.snapshot'])
        self.assertEqual(submissions, 0)

    def test_mail_timeout_never_resubmits(self):
        status, report, requests, submissions = self.run_flow('timeout')
        self.assertEqual(status, 1)
        self.assertEqual(report['stoppedAt'], 'mail-timeout')
        self.assertEqual(submissions, 1)
