"""Local browser interception exercises actual conversation event listeners."""
import contextlib
import io
import json
import os
import tempfile
import time
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
from browser_fixture import CamoufoxBrowserTests
from executor.platforms.claude import conversation

class CompletionRequestTests(CamoufoxBrowserTests):


    def run_response(self, status, body, content_type='application/json', unrelated=False, abort=False, actual_wait=False):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bundle = root/'bundles'/'fixture'
            (bundle/'profile').mkdir(parents=True)
            (bundle/'fingerprint.json').write_text('{}')
            cfg = SimpleNamespace(paths=SimpleNamespace(bundles=root/'bundles', artifacts=root/'artifacts'), timing=SimpleNamespace(cf_timeout=1, reply_timeout=2))
            context = self.browser.new_context()
            endpoint = '/api/organizations/fixture/chat_conversations/fixture/completion' if not unrelated else '/api/settings'
            html = f"""<textarea aria-label="Message" onkeydown="this.dataset.lastKey=event.key;if(event.key==='Enter'){{this.dataset.didEnter=true;try{{const result=fetch('{endpoint}',{{method:'POST'}});this.dataset.started=true;result.then(r=>r.text()).then(()=>this.dataset.fetchDone=true).catch(e=>this.dataset.fetchDone=e.name);}}catch(e){{this.dataset.fetchError=e.name;}}}}"></textarea><button>Send</button>"""
            def route_fixture(route):
                if route.request.url.endswith(endpoint):
                    if abort:
                        route.abort()
                        return
                    route.fulfill(status=status, content_type=content_type, body=body)
                else:
                    route.fulfill(status=200, content_type='text/html', body=html)
            context.route('**/*', route_fixture)
            page = context.new_page()
            real_wait = page.wait_for_timeout
            finished = []
            signals = []
            page.on('request', lambda r: signals.append(('request', r.method)))
            page.on('response', lambda r: signals.append(('response', r.status)))
            page.on('requestfinished', lambda request: finished.append(request.url) if request.method == 'POST' else None)
            page.on('requestfailed', lambda request: finished.append(request.url) if request.method == 'POST' else None)
            page.wait_for_timeout = lambda _: None
            @contextlib.contextmanager
            def local_browser(**kwargs):
                try:
                    yield context
                finally:
                    context.close()
            real_reply_wait = conversation.wait_for_reply_completion
            def stable_reply(page, **kwargs):
                deadline = time.monotonic() + cfg.timing.reply_timeout
                while time.monotonic() < deadline:
                    observer = kwargs.get('completion')
                    if finished and (observer is None or not observer.pending):
                        break
                    real_wait(10)
                self.assertTrue(finished, "input_length=" + str(len(page.get_by_role('textbox').input_value())) + "; last_key=" + str(page.get_by_role('textbox').get_attribute('data-last-key')) + '; events=' + repr(signals) + '; fetch_state=' + repr(page.get_by_role('textbox').evaluate('(el) => ({...el.dataset})')))
                if observer is not None:
                    self.assertFalse(observer.pending)
                if actual_wait:
                    return real_reply_wait(page, **kwargs)
                return True, 'local stable snapshot'
            with contextlib.chdir(root), patch.object(conversation,'CFG',cfg), patch.object(conversation,'Camoufox',local_browser), patch.object(conversation,'load_fingerprint',return_value={}), patch.object(conversation,'camoufox_kwargs',return_value={}), patch.object(conversation,'wait_for_reply_completion',stable_reply), contextlib.redirect_stdout(io.StringIO()):
                rc = conversation.execute_conversation_step(['--account','fixture','--turns','1'])
            files = list((root/'artifacts').glob('*/report.json'))
            self.assertEqual(len(files),1)
            return rc,json.loads(files[0].read_text())

    def test_http_503_is_not_reply_success(self):
        rc,report = self.run_response(503,'Unavailable','text/plain')
        self.assertEqual(rc,1)
        self.assertEqual(report['stoppedAt'],'completion-http-error')
        self.assertFalse(report['turns'][0]['replied'])

    def test_rate_limit_is_failure_not_account_block(self):
        rc,report = self.run_response(429,'{"details":{"error_code":"rate_limit"}}')
        self.assertEqual(rc,1)
        self.assertNotIn('blocked',report)
        self.assertEqual(report['stoppedAt'],'completion-rate-limited')

    def test_json_error_under_http_200_fails(self):
        rc,report = self.run_response(200,'{"error":{"message":"fixture error"}}')
        self.assertEqual(rc,1)
        self.assertEqual(report['stoppedAt'],'completion-error')

    def test_malformed_declared_json_fails(self):
        rc,report = self.run_response(200,'{broken')
        self.assertEqual(rc,1)
        self.assertEqual(report['stoppedAt'],'completion-invalid-json')

    def test_error_code_in_normal_stream_text_does_not_block(self):
        rc,report = self.run_response(200,'data: {"type":"content_block_delta","delta":{"text":"verification_required is an example string"}}\n\nevent: message_stop\ndata: {"type":"message_stop"}\n\n','text/event-stream')
        self.assertEqual(rc,0)
        self.assertNotIn('blocked',report)

    def test_explicit_verification_error_stays_blocked(self):
        rc,report = self.run_response(200,'{"details":{"error_code":"verification_required"}}')
        self.assertEqual(rc,2)
        self.assertEqual(report['blocked'],'verification_required')

    def test_unrelated_http_failure_does_not_mark_reply_failed(self):
        rc,report = self.run_response(503,'Unavailable','text/plain',unrelated=True)
        self.assertEqual(rc,0)
        self.assertNotIn('blocked',report)

    def test_ui_stability_requires_finished_transport(self):
        from executor.platforms.claude.completion_observer import CompletionObserver
        context = self.browser.new_context(offline=True)
        self.addCleanup(context.close)
        page = context.new_page()
        page.wait_for_timeout = lambda _: None
        page.set_content('<article>' + 'stable local output ' * 10 + '</article>')
        observer = CompletionObserver()
        ok, _ = conversation.wait_for_reply_completion(page, timeout=0.1, completion=observer)
        self.assertFalse(ok)
        request = type('Request', (), {'method':'POST', 'url':'https://claude.ai/api/organizations/fixture/chat_conversations/fixture/completion'})()
        response = SimpleNamespace(request=request, status=200, headers={'content-type':'text/event-stream'}, body=lambda: b'data: {"type":"message_stop"}\n\n')
        request.response = lambda: response
        observer.on_request_finished(request)
        ok, _ = conversation.wait_for_reply_completion(page, timeout=0.1, completion=observer)
        self.assertTrue(ok)

    def test_network_abort_is_reported_as_request_failure(self):
        rc, report = self.run_response(200, '', abort=True)
        self.assertEqual(rc, 1)
        self.assertEqual(report['stoppedAt'], 'completion-network-error')
        self.assertFalse(report['turns'][0]['replied'])

    def test_camoufox_reads_terminal_body_through_actual_wait(self):
        body = 'event: message_stop\ndata: {"type":"message_stop"}\n\n'
        rc, report = self.run_response(200, body, 'text/event-stream', actual_wait=True)
        self.assertEqual(rc, 0)
        self.assertTrue(report['turns'][0]['completion']['terminal_received'])
        self.assertEqual(report['turns'][0]['completion']['event_count'], 1)

    def test_camoufox_rejects_stream_without_terminal(self):
        body = 'data: {"type":"content_block_delta","delta":{"text":"fixture"}}\n\n'
        rc, report = self.run_response(200, body, 'text/event-stream', actual_wait=True)
        self.assertEqual(rc, 1)
        self.assertEqual(report['stoppedAt'], 'completion-missing-terminal')

    def test_camoufox_rejects_http_200_stream_error(self):
        body = 'event: error\ndata: {"type":"error","error":{"type":"overloaded_error"}}\n\n'
        rc, report = self.run_response(200, body, 'text/event-stream', actual_wait=True)
        self.assertEqual(rc, 1)
        self.assertEqual(report['stoppedAt'], 'completion-stream-error')


class CompletionObserverUnitTests(unittest.TestCase):
    def make_request(self, status=200, mime='application/json', payload=None):
        request = type('Request', (), {'method':'POST', 'url':'https://claude.ai/api/organizations/fixture/chat_conversations/fixture/completion'})()
        response = SimpleNamespace(request=request, status=status, headers={'content-type':mime}, json=lambda: payload, body=lambda: b'data: {"type":"message_stop"}\n\n')
        request.response = lambda: response
        return request, response

    def test_json_read_occurs_only_after_request_finished(self):
        from executor.platforms.claude.completion_observer import CompletionObserver
        observer = CompletionObserver()
        request, response = self.make_request()
        observer.on_request(request)
        def read_json():
            self.assertIn(request, observer.pending)
            return {'error': {'message': 'secret-fixture-not-for-log'}}
        response.json = read_json
        observer.on_response(response)
        self.assertIsNone(observer.failure)
        observer.on_request_finished(request)
        self.assertEqual(observer.failure['reason'], 'completion-error')
        self.assertNotIn('secret-fixture', repr(observer.failure))
        self.assertFalse(observer.pending)

    def test_reset_does_not_reuse_previous_transport(self):
        from executor.platforms.claude.completion_observer import CompletionObserver
        observer = CompletionObserver()
        request, _ = self.make_request(mime='text/event-stream')
        observer.on_request_finished(request)
        self.assertTrue(observer.transport_finished)
        observer.reset()
        self.assertFalse(observer.transport_finished)
        self.assertIsNone(observer.failure)

    def test_wrong_origin_method_and_endpoint_are_ignored(self):
        from executor.platforms.claude.completion_observer import CompletionObserver
        observer = CompletionObserver()
        request, _ = self.make_request()
        for method, url in [('GET', request.url), ('POST', request.url.replace('claude.ai', 'example.invalid')), ('POST', 'https://claude.ai/api/completion-history')]:
            with self.subTest(method=method, url=url):
                request.method, request.url = method, url
                observer.on_request(request)
                observer.on_request_failed(request)
                self.assertFalse(observer.pending)
                self.assertIsNone(observer.failure)

    def test_unknown_error_code_is_not_copied_to_report(self):
        from executor.platforms.claude.completion_observer import CompletionObserver
        observer = CompletionObserver()
        request, _ = self.make_request(payload={'details': {'error_code': 'secret-fixture'}})
        observer.on_request_finished(request)
        self.assertEqual(observer.failure['reason'], 'completion-error')
        self.assertNotIn('secret-fixture', repr(observer.failure))

    def test_non_object_json_is_not_accepted_as_stream(self):
        from executor.platforms.claude.completion_observer import CompletionObserver
        observer = CompletionObserver()
        request, _ = self.make_request(payload=[])
        observer.on_request_finished(request)
        self.assertEqual(observer.failure['reason'], 'completion-unexpected-response')
        self.assertFalse(observer.transport_finished)
