import json
import tempfile
import unittest
from pathlib import Path
from executor.contracts.job import JobSpec
from executor.platforms.claude.adapter import ClaudePlatform

class CurrentTaskCredentialsTests(unittest.TestCase):
    def test_historical_artifacts_never_become_current_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'bundles/fixture').mkdir(parents=True)
            oauth = root / 'artifacts/oauth-fixture-old'
            oauth.mkdir(parents=True)
            (oauth / 'credentials.json').write_text(json.dumps({'accessToken': 'old-access', 'refreshToken': 'old-refresh', 'expiresAt': 1}))
            check = root / 'artifacts/check-fixture-old'
            check.mkdir()
            (check / 'report.json').write_text(json.dumps({'summary': {'health': 'banned', 'plan': 'old-plan'}}))
            creds = ClaudePlatform()._collect_credentials(JobSpec('claude', 'fixture', ['warmup']), root).to_json()
            self.assertNotIn('accessToken', creds)
            self.assertNotIn('health', creds)
            self.assertNotIn('plan', creds)
