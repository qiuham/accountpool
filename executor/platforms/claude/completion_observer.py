"""观测对话请求、结构化错误与 SSE message_stop 终止证据。"""
from urllib.parse import urlparse
import re
import io
import json
from sseclient import SSEClient
from playwright.sync_api import Error


class CompletionObserver:
    def __init__(self):
        self.reset()

    def reset(self):
        self.pending = set()
        self.transport_finished = False
        self.terminal_received = False
        self.event_count = 0
        self.failure = None

    @staticmethod
    def is_completion_request(request):
        url = urlparse(request.url)
        return (request.method == "POST" and url.scheme == "https"
                and url.hostname == "claude.ai"
                and re.fullmatch(r"/api/organizations/[^/]+/chat_conversations/[^/]+/completion/?", url.path) is not None)

    def _fail(self, reason, *, status=None, code=None):
        self.failure = {"reason": reason, "http_status": status}
        if code in ("verification_required", "kyc_pending", "rate_limit"):
            self.failure["code"] = code

    def on_request(self, request):
        if self.is_completion_request(request):
            self.pending.add(request)

    def on_response(self, response):
        if not self.is_completion_request(response.request):
            return
        if response.status == 429:
            self._fail("completion-rate-limited", status=429)
        elif response.status >= 400:
            self._fail("completion-http-error", status=response.status)

    def on_request_failed(self, request):
        if self.is_completion_request(request):
            self.pending.discard(request)
            self._fail("completion-network-error")

    def on_request_finished(self, request):
        if not self.is_completion_request(request):
            return
        try:
            response = request.response()
            if response is None:
                self._fail("completion-missing-response")
                return
            self.on_response(response)
            mime = response.headers.get("content-type", "").split(";", 1)[0].strip().lower()
            if mime == "application/json" or mime.endswith("+json"):
                try:
                    payload = response.json()
                except ValueError:
                    if self.failure is None:
                        self._fail("completion-invalid-json", status=response.status)
                    return
                if not isinstance(payload, dict):
                    if self.failure is None:
                        self._fail("completion-unexpected-response", status=response.status)
                    return
                if not self._observe_payload_error(payload, response.status) and self.failure is None:
                    self._fail("completion-unexpected-response", status=response.status)
            elif mime == "text/event-stream" and 200 <= response.status < 300:
                body = response.body()
                self.transport_finished = True
                self._inspect_sse(body, response.status)
            elif self.failure is None:
                    self._fail("completion-unexpected-response", status=response.status)
            elif mime == "text/event-stream" and 200 <= response.status < 300:
                self.transport_finished = True
            elif self.failure is None:
                self._fail("completion-unexpected-response", status=response.status)
        except Error:
            if self.failure is None:
                self._fail("completion-response-unreadable")
        finally:
            self.pending.discard(request)

    def _observe_payload_error(self, payload, status, *, reason="completion-error"):
        details = payload.get("details")
        code = details.get("error_code") if isinstance(details, dict) else None
        if code in ("verification_required", "kyc_pending"):
            self._fail("completion-verification-required", status=status, code=code)
        elif code == "rate_limit":
            self._fail("completion-rate-limited", status=status, code=code)
        elif code or payload.get("error") or payload.get("type") == "error":
            self._fail(reason, status=status)
        else:
            return False
        return True

    def _inspect_sse(self, body, status):
        try:
            for event in SSEClient(io.BytesIO(body)).events():
                self.event_count += 1
                try:
                    payload = json.loads(event.data)
                except ValueError:
                    self._fail("completion-invalid-event", status=status)
                    return
                if not isinstance(payload, dict):
                    self._fail("completion-invalid-event", status=status)
                    return
                if self._observe_payload_error(payload, status, reason="completion-stream-error"):
                    return
                if event.event == "error":
                    self._fail("completion-stream-error", status=status)
                    return
                if payload.get("type") == "message_stop":
                    self.terminal_received = True
        except UnicodeError:
            self._fail("completion-invalid-event", status=status)
            return
        if not self.terminal_received:
            self._fail("completion-missing-terminal", status=status)

    @property
    def reply_completed(self):
        return (self.transport_finished and self.terminal_received
                and self.failure is None and not self.pending)

    def evidence(self):
        return {"transport_finished": self.transport_finished,
                "terminal_received": self.terminal_received,
                "event_count": self.event_count}

    @property
    def blocked_code(self):
        code = self.failure.get("code") if self.failure else None
        return code if code in ("verification_required", "kyc_pending") else None
