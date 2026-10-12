"""仅使用合成 SSE 字节，不复用任何上游凭据或正文。"""
import unittest
from types import SimpleNamespace
from executor.platforms.claude.completion_observer import CompletionObserver


class SSECompletionTests(unittest.TestCase):
    def observe(self, body):
        request = type('Request', (), {'method':'POST', 'url':'https://claude.ai/api/organizations/fixture/chat_conversations/fixture/completion'})()
        response = SimpleNamespace(request=request, status=200,
                                   headers={'content-type':'text/event-stream; charset=utf-8'},
                                   body=lambda: body.encode('utf-8'))
        request.response = lambda: response
        observer = CompletionObserver()
        observer.on_request(request)
        observer.on_request_finished(request)
        return observer

    def test_message_stop_is_successful_terminal(self):
        observer = self.observe('event: message_stop\ndata: {"type":"message_stop"}\n\n')
        self.assertIsNone(observer.failure)
        self.assertTrue(observer.transport_finished)
        self.assertFalse(observer.pending)

    def test_closed_stream_without_terminal_fails(self):
        observer = self.observe('event: content_block_delta\ndata: {"type":"content_block_delta","delta":{"text":"fixture"}}\n\n')
        self.assertEqual(observer.failure['reason'], 'completion-missing-terminal')

    def test_content_block_stop_is_not_message_stop(self):
        observer = self.observe('event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n')
        self.assertEqual(observer.failure['reason'], 'completion-missing-terminal')

    def test_http_200_stream_error_fails(self):
        observer = self.observe('event: error\ndata: {"type":"error","error":{"type":"overloaded_error","message":"private-fixture"}}\n\n')
        self.assertEqual(observer.failure['reason'], 'completion-stream-error')
        self.assertNotIn('private-fixture', repr(observer.failure))

    def test_invalid_event_json_fails(self):
        observer = self.observe('event: message_stop\ndata: {broken\n\n')
        self.assertEqual(observer.failure['reason'], 'completion-invalid-event')

    def test_multiline_data_and_crlf_are_parsed(self):
        observer = self.observe(': local comment\r\nevent: message_stop\r\ndata: {"type":\r\ndata: "message_stop"}\r\n\r\n')
        self.assertIsNone(observer.failure)
        self.assertTrue(observer.transport_finished)

    def test_error_after_terminal_overrides_success(self):
        observer = self.observe('event: message_stop\ndata: {"type":"message_stop"}\n\nevent: error\ndata: {"type":"error"}\n\n')
        self.assertEqual(observer.failure['reason'], 'completion-stream-error')

    def test_empty_stream_is_not_success(self):
        observer = self.observe('')
        self.assertEqual(observer.failure['reason'], 'completion-missing-terminal')

    def test_done_sentinel_is_not_a_compatibility_terminal(self):
        observer = self.observe('data: [DONE]\n\n')
        self.assertIsNotNone(observer.failure)
