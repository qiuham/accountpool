import contextlib
import io
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from executor.contracts.job import Credentials, JobSpec
from executor.platforms.claude.adapter import ClaudePlatform


class ResultRegressionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.platform = ClaudePlatform()
        self.spec = JobSpec('claude', 'fixture', ['warmup'])

    def classify(self, report):
        (self.root / 'report.json').write_text(json.dumps(report))
        return self.platform._classify_step_failure(self.spec, str(self.root))

    def test_spa_timeout_is_not_proof_of_cloudflare(self):
        self.assertEqual(self.classify({'stoppedAt': 'spa-not-rendered'}), 'page-not-ready')

    def test_expired_session_is_recoverable(self):
        self.assertEqual(self.classify({'stoppedAt': 'no-session-key'}), 'blocked:session-expired')

    def test_explicit_cf_challenge_requires_human(self):
        self.assertEqual(self.classify({'stoppedAt': 'cf-challenge', 'pageState': {'human_action_required': True}}),
                         'blocked:cf-challenge-human-required')

    def test_automatic_challenge_is_not_labeled_human(self):
        self.assertEqual(self.classify({'stoppedAt': 'cf-challenge', 'pageState': {'human_action_required': False}}),
                         'blocked:cf-challenge-not-passed')

    def test_rate_limit_is_not_transient_network_error(self):
        self.assertEqual(self.classify({'stoppedAt': 'http-error', 'pageState': {'http_status': 429}}),
                         'blocked:rate-limited')

    def test_access_denied_does_not_trigger_blind_retry(self):
        self.assertEqual(self.classify({'stoppedAt': 'access-denied'}), 'blocked:access-denied')

    def test_missing_artifact_is_not_proof_of_missing_bundle(self):
        self.assertEqual(self.platform._classify_step_failure(self.spec, None), 'no-artifacts')

    def test_false_observation_is_preserved(self):
        self.assertIs(Credentials(extra={'subscribed': False}).to_json()['subscribed'], False)

    def test_old_report_is_not_reused_after_a_crash(self):
        old = self.root / 'artifacts' / 'warmup-fixture-old'
        old.mkdir(parents=True)
        (old / 'report.json').write_text(json.dumps({'summary': {'health': 'banned'}}))
        os.utime(old, (1, 1))
        with patch('executor.platforms.claude.adapter.subprocess.run') as run:
            run.return_value.returncode = 1
            with contextlib.redirect_stdout(io.StringIO()):
                _, artifact, reason = self.platform._execute_step('warmup', self.spec, self.root, {})
        self.assertIsNone(artifact)
        self.assertEqual(reason, 'no-artifacts')

    def test_mail_credential_does_not_enter_console(self):
        spec = JobSpec('claude', 'fixture', ['register'], email='fixture@example.test',
                       options={'mail_jwt': 'fixture-secret-not-real'})
        stdout = io.StringIO()
        with patch('executor.platforms.claude.adapter.subprocess.run') as run:
            run.return_value.returncode = 1
            with contextlib.redirect_stdout(stdout):
                self.platform._execute_step('register', spec, self.root, {})
        self.assertNotIn('fixture-secret-not-real', stdout.getvalue())


if __name__ == '__main__':
    unittest.main()
