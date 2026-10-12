import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from playwright.sync_api import sync_playwright
from executor.runtime.http_trace import attach_http_trace

class TraceTests(unittest.TestCase):
    def test_browser_exchange_and_network_error(self):
        with tempfile.TemporaryDirectory() as directory, sync_playwright() as p:
            path = Path(directory, 'trace.jsonl')
            path.touch(mode=0o600)
            browser = p.chromium.launch(executable_path='/home/ham/.cache/ms-playwright/chromium-1234/chrome-linux64/chrome')
            try:
                page = browser.new_page()
                page.route('**/*', lambda route: route.abort() if route.request.url.endswith('/fail') else route.fulfill(status=200,content_type='application/json',body='{"ok":true,"sessionKey":"private-fixture"}'))
                with patch.dict(os.environ, {'ACCOUNTPOOL_TRACE_FILE':str(path)}):
                    attach_http_trace(page)
                    page.goto('http://fixture.test/api')
                    page.wait_for_timeout(100)
                    try:
                        page.goto('http://fixture.test/fail')
                    except Exception:
                        pass
                    page.wait_for_timeout(100)
                raw = path.read_text()
                self.assertNotIn('private-fixture', raw)
                events = [json.loads(line) for line in raw.splitlines()]
                self.assertTrue(any(e['response'] and e['response']['body']['ok'] for e in events))
                self.assertTrue(any(e.get('error') and e['response'] is None for e in events))
            finally:
                browser.close()
