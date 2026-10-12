"""Diagnostics must not materialize unknown-sized or compressed responses."""
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch
from executor.runtime.http_trace import attach_http_trace

class TraceMemoryTests(unittest.TestCase):
    def test_unbounded_bodies_are_not_read(self):
        for headers in ({'content-type':'text/event-stream'},
                        {'content-type':'application/json'},
                        {'content-type':'application/json','content-length':'40000'},
                        {'content-type':'application/json','content-length':'10','content-encoding':'gzip'},
                        {'content-type':'application/json','content-length':'invalid'}):
            with self.subTest(headers=headers), tempfile.TemporaryDirectory() as tmp:
                path=Path(tmp)/'trace.jsonl'; path.touch()
                page=Mock(); handlers={}
                page.on.side_effect=lambda event, fn: handlers.__setitem__(event, fn)
                request=Mock(resource_type='fetch',url='https://fixture.test/api',method='GET',post_data_buffer=None,failure=None)
                request.all_headers.return_value={}
                response=request.response.return_value
                response.status=200; response.all_headers.return_value=headers
                response.body.return_value=b'{}'
                with patch.dict(os.environ, ACCOUNTPOOL_TRACE_FILE=str(path)):
                    attach_http_trace(page); handlers['request'](request); handlers['requestfinished'](request)
                response.body.assert_not_called()
                self.assertTrue(json.loads(path.read_text())['response']['body']['omitted'])
