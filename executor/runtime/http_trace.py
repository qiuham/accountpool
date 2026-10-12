"""Passive task-scoped, bounded HTTP diagnostics. Never alters requests."""
import json
import os
import re
import time
from datetime import datetime, timezone
from urllib.parse import urlsplit, urlunsplit

TOKEN = re.compile(r"sk-[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|\b\d{6}\b")
URL = re.compile(r"https?://[^\s\"'<>]+")

def clean_url(value):
    try:
        u = urlsplit(value)
        host = u.hostname or ""
        if u.port:
            host += ":" + str(u.port)
        return urlunsplit((u.scheme, host, u.path, "redacted" if u.query else "", ""))
    except ValueError:
        return "[URL]"

def clean(value):
    if isinstance(value, dict):
        result = {}
        for key, item in value.items():
            lower = key.lower()
            sensitive = any(part in lower for part in ("cookie", "auth", "token", "password", "secret", "session", "credential", "jwt", "proof"))
            result[key] = "[REDACTED]" if sensitive or lower in ("code", "key", "raw", "html", "text", "content", "link") else clean(item)
        return result
    if isinstance(value, list):
        return [clean(v) for v in value]
    if isinstance(value, str):
        return URL.sub(lambda m: clean_url(m.group()), TOKEN.sub("[REDACTED]", value))
    return value

def payload(raw):
    if not raw:
        return None
    if len(raw) > 32768:
        return {"omitted": True, "bytes": len(raw), "reason": "body exceeds 32 KiB"}
    try:
        return clean(json.loads(raw))
    except (ValueError, TypeError):
        return {"omitted": True, "bytes": len(raw), "reason": "non-JSON body"}

def attach_http_trace(page):
    path = os.environ.get("ACCOUNTPOOL_TRACE_FILE")
    if not path:
        return
    starts = {}
    count = 0

    def begin(req):
        if req.resource_type in ("xhr", "fetch", "document") and req.url.startswith(("https://", "http://")):
            starts[req] = (time.monotonic(), datetime.now(timezone.utc).isoformat())

    def finish(req):
        nonlocal count
        start = starts.pop(req, None)
        if start is None or count >= 200:
            return
        try:
            event = {"source": "browser", "at": start[1], "durationMs": round((time.monotonic()-start[0])*1000),
                     "request": {"method": req.method, "url": clean_url(req.url),
                                 "headers": clean(req.all_headers()), "body": payload(req.post_data_buffer)},
                     "response": None}
            response = req.response()
            if response is not None:
                headers = response.all_headers()
                body = {"omitted": True, "reason": "body unavailable"}
                # Missing length is NOT a zero-byte body. Playwright body()
                # materializes the whole decoded response; never do that for
                # streams, unknown lengths, or compressed diagnostic payloads.
                length = headers.get("content-length", "")
                mime = headers.get("content-type", "").split(";", 1)[0].strip().lower()
                bounded_json = (
                    length.isdecimal() and int(length) <= 32768
                    and (mime == "application/json" or mime.endswith("+json"))
                    and headers.get("content-encoding", "identity").lower() == "identity"
                )
                if bounded_json:
                    try:
                        body = payload(response.body())
                    except Exception:
                        pass
                else:
                    body = {"omitted": True, "reason": "body is not bounded uncompressed JSON"}
                event["response"] = {"status": response.status, "headers": clean(headers), "body": body}
            if req.failure:
                event["error"] = clean(req.failure)
            raw = (json.dumps(event, ensure_ascii=False) + "\n").encode()
            fd = os.open(path, os.O_APPEND | os.O_WRONLY)
            try:
                if os.fstat(fd).st_size < 4 * 1024 * 1024:
                    os.write(fd, raw)
                    count += 1
            finally:
                os.close(fd)
        except Exception:
            # Diagnostics must never alter browser workflow.
            return
    page.on("request", begin)
    page.on("requestfinished", finish)
    page.on("requestfailed", finish)
