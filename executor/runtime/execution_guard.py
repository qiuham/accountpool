"""Cross-process execution lease shared with the Go runner (Linux flock).

The parent passes its open lock descriptor to workers. Standalone commands
acquire the same lock and enforce the same 60-second interval themselves.
"""
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import os
from pathlib import Path
import time

LOCK_FD_ENV = 'ACCOUNTPOOL_LOCK_FD'
PROJECT_ROOT = Path(__file__).resolve().parents[2]


@contextmanager
def execution_guard(project_root: Path = PROJECT_ROOT, *, cooldown: float = 60):
    jobs = Path(project_root).resolve() / 'artifacts' / 'jobs'
    jobs.mkdir(parents=True, exist_ok=True, mode=0o700)
    lock_path = jobs / '.executor.lock'
    inherited = os.environ.get(LOCK_FD_ENV)
    if inherited is not None:
        fd = int(inherited)
        descriptor = os.fstat(fd)
        target = lock_path.stat()
        if (descriptor.st_dev, descriptor.st_ino) != (target.st_dev, target.st_ino):
            raise RuntimeError('inherited executor lock does not match project')
        # A shared open file description retains the parent's flock.
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield fd
        return

    fd = os.open(lock_path, os.O_CREAT | os.O_RDWR, 0o600)
    acquired = False
    started = False
    finished_path = jobs / '.last-finished'

    def record_time():
        temporary = jobs / f'.last-finished-{os.getpid()}'
        stamp = datetime.now(timezone.utc).isoformat(timespec='microseconds')
        with os.fdopen(os.open(temporary, os.O_CREAT | os.O_WRONLY | os.O_TRUNC, 0o600), 'w') as out:
            out.write(stamp)
        temporary.replace(finished_path)

    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise RuntimeError('executor already running') from error
        acquired = True
        if finished_path.exists():
            finished = datetime.fromisoformat(finished_path.read_text().strip().replace('Z', '+00:00'))
            if finished.tzinfo is None:
                raise ValueError('cooldown timestamp must include timezone')
            wait = finished.timestamp() + cooldown - time.time()
            if wait > 0:
                print(f'执行冷却：等待 {wait:.1f} 秒', flush=True)
                time.sleep(wait)
        record_time()
        started = True
        os.environ[LOCK_FD_ENV] = str(fd)
        yield fd
    finally:
        try:
            if started:
                os.environ.pop(LOCK_FD_ENV, None)
                record_time()
        finally:
            if acquired:
                fcntl.flock(fd, fcntl.LOCK_UN)
            os.close(fd)
