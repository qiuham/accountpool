"""Task-scoped IPC to Go. No mailbox credentials, HTTP, polling or parsing here."""
import json
import os
import socket

RPC_FD_ENV = 'ACCOUNTPOOL_RPC_FD'


class BrowserServiceError(RuntimeError):
    def __init__(self, reason):
        self.reason = reason
        super().__init__(reason)


class BrowserServices:
    def __init__(self):
        descriptor = os.environ.get(RPC_FD_ENV)
        if descriptor is None:
            raise BrowserServiceError('mailbox-go-service-required')
        self.socket = socket.socket(fileno=os.dup(int(descriptor)))
        self.socket.settimeout(650)
        self.stream = self.socket.makefile('rwb')

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()

    def close(self):
        self.stream.close()
        self.socket.close()

    def call(self, method, **params):
        try:
            self.stream.write(json.dumps({'method': method, **params}).encode() + b'\n')
            self.stream.flush()
            raw = self.stream.readline(8193)
            if not raw or len(raw) > 8192:
                raise BrowserServiceError('mailbox-ipc-invalid-response')
            response = json.loads(raw)
            if not isinstance(response, dict):
                raise BrowserServiceError('mailbox-ipc-invalid-response')
            if response.get('error'):
                raise BrowserServiceError(response['error'])
            if response.get('ok') is not True:
                raise BrowserServiceError('mailbox-ipc-invalid-response')
            return response
        except (OSError, ValueError) as error:
            raise BrowserServiceError('mailbox-ipc-disconnected') from error
