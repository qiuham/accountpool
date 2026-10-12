import unittest
from unittest.mock import MagicMock, patch
from executor.platforms.claude.registration import click_login_action

class LoginActionTests(unittest.TestCase):
    def test_click_failure_does_not_dispatch_another_action(self):
        page = MagicMock()
        page.get_by_role.return_value.count.return_value = 1
        el = page.get_by_role.return_value.nth.return_value
        el.click.side_effect = RuntimeError('fixture click outcome unknown')
        with patch('executor.platforms.claude.registration.expect'):
            with self.assertRaisesRegex(RuntimeError, 'outcome unknown'):
                click_login_action(page, '^Continue$')
        self.assertEqual(el.click.call_count, 1)
        self.assertEqual(page.get_by_role.call_count, 1)

    def test_successful_click_does_not_read_destroyed_element(self):
        page = MagicMock()
        page.get_by_role.return_value.count.return_value = 1
        el = page.get_by_role.return_value.nth.return_value
        with patch('executor.platforms.claude.registration.expect'):
            self.assertTrue(click_login_action(page, '^Continue$'))
        el.inner_text.assert_not_called()
