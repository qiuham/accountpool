import fcntl
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

from executor.runtime.execution_guard import execution_guard, LOCK_FD_ENV


class ExecutionGuardTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env = patch.dict(os.environ)
        self.env.start()
        self.addCleanup(self.env.stop)
        os.environ.pop(LOCK_FD_ENV, None)

    def test_competing_process_is_rejected(self):
        code = 'from pathlib import Path;from executor.runtime.execution_guard import execution_guard;import sys\nwith execution_guard(Path(sys.argv[1]),cooldown=0):print("entered")'
        with execution_guard(self.root, cooldown=0):
            env = dict(os.environ)
            env.pop(LOCK_FD_ENV)
            result = subprocess.run([sys.executable, '-c', code, str(self.root)], env=env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('executor already running', result.stderr)
        self.assertNotIn('entered', result.stdout)

    def test_inherited_lock_is_not_released_by_child(self):
        code = 'from pathlib import Path;from executor.runtime.execution_guard import execution_guard;import sys\nwith execution_guard(Path(sys.argv[1])):print("inherited")'
        with execution_guard(self.root, cooldown=0) as fd:
            child = subprocess.run([sys.executable, '-c', code, str(self.root)], pass_fds=(fd,), capture_output=True, text=True)
            self.assertEqual(child.returncode, 0, child.stderr)
            self.assertEqual(child.stdout.strip(), 'inherited')
            with open(self.root / 'artifacts/jobs/.executor.lock') as other:
                with self.assertRaises(BlockingIOError):
                    fcntl.flock(other, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def test_second_run_observes_cooldown(self):
        with execution_guard(self.root, cooldown=0):
            pass
        start = time.monotonic()
        with execution_guard(self.root, cooldown=0.04):
            pass
        self.assertGreaterEqual(time.monotonic() - start, 0.035)

    def test_failure_records_finish_and_releases_lock(self):
        with self.assertRaisesRegex(RuntimeError, 'fixture'):
            with execution_guard(self.root, cooldown=0):
                raise RuntimeError('fixture')
        self.assertTrue((self.root / 'artifacts/jobs/.last-finished').is_file())
        self.assertNotIn(LOCK_FD_ENV, os.environ)
        with execution_guard(self.root, cooldown=0):
            pass

    def test_invalid_record_fails_closed(self):
        jobs = self.root / 'artifacts/jobs'
        jobs.mkdir(parents=True)
        (jobs / '.last-finished').write_text('broken timestamp')
        with self.assertRaises(ValueError):
            with execution_guard(self.root):
                self.fail('invalid record allowed execution')
        self.assertEqual((jobs / '.last-finished').read_text(), 'broken timestamp')

    def test_wrong_inherited_descriptor_fails_closed(self):
        jobs = self.root / 'artifacts/jobs'
        jobs.mkdir(parents=True)
        (jobs / '.executor.lock').touch()
        with tempfile.TemporaryFile() as other:
            os.environ[LOCK_FD_ENV] = str(other.fileno())
            with self.assertRaisesRegex(RuntimeError, 'does not match'):
                with execution_guard(self.root):
                    self.fail('accepted unrelated fd')
