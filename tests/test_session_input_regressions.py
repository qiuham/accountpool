import json
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from executor.contracts.job import JobSpec
from executor.platforms.claude.session_cookies import read_session_cookies
from executor.platforms.claude.adapter import ClaudePlatform

class SessionInputTests(unittest.TestCase):
    def test_all_readers_observe_committed_wal(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            bundle = root / "bundles" / "fixture"
            profile = bundle / "profile"
            profile.mkdir(parents=True)
            con = sqlite3.connect(profile / "cookies.sqlite")
            self.addCleanup(con.close)
            con.execute("create table moz_cookies (name text, value text, host text)")
            con.commit()
            con.execute("pragma journal_mode=wal")
            con.execute("pragma wal_autocheckpoint=0")
            con.execute("insert into moz_cookies values ('sessionKey','fixture-wal-value','.claude.ai')")
            con.commit()
            self.assertEqual(read_session_cookies(bundle)["sessionKey"], "fixture-wal-value")
            con.execute("insert into moz_cookies values ('sessionKey','wrong-domain','example.com')")
            con.commit()
            adapter = ClaudePlatform()
            credentials = adapter._collect_credentials(JobSpec("claude", "fixture", ["session"]), root)
            self.assertEqual(credentials.session_key, "fixture-wal-value")

    def test_unknown_job_field_is_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            file = Path(root) / "job.json"
            file.write_text(json.dumps(dict(platform="claude", account="fixture", steps=["check"], artifact_rooot="typo")))
            with self.assertRaisesRegex(ValueError, "artifact_rooot"):
                JobSpec.from_json(file)

    def test_empty_or_mistyped_steps_are_rejected(self):
        for steps in ([], "check", [1], [""]):
            with self.subTest(steps=steps), self.assertRaises(ValueError):
                JobSpec("claude", "fixture", steps)

    def test_missing_cookie_database_is_not_created(self):
        from executor.platforms.claude.session_cookies import read_session_cookies
        with tempfile.TemporaryDirectory() as root:
            bundle = Path(root)
            self.assertEqual(read_session_cookies(bundle), {})
            self.assertFalse((bundle / 'profile').exists())

    def test_cookie_connection_is_closed_after_read(self):
        from executor.platforms.claude.session_cookies import read_session_cookies
        with tempfile.TemporaryDirectory() as root:
            bundle = Path(root)
            connection = unittest.mock.MagicMock()
            connection.execute.return_value = [('sessionKey', 'fixture')]
            with patch('executor.platforms.claude.session_cookies.sqlite3.connect', return_value=connection), patch.object(Path, 'is_file', return_value=True):
                self.assertEqual(read_session_cookies(bundle), {'sessionKey': 'fixture'})
            connection.close.assert_called_once()

    def test_job_root_and_options_require_objects(self):
        with tempfile.TemporaryDirectory() as root:
            file = Path(root) / 'job.json'
            file.write_text('[]')
            with self.assertRaisesRegex(ValueError, 'JSON object'):
                JobSpec.from_json(file)
        with self.assertRaisesRegex(ValueError, 'options'):
            JobSpec('claude', 'fixture', ['check'], options=[])
