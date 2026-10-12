import unittest
from types import SimpleNamespace
from unittest.mock import Mock, patch
from executor.platforms.claude.conversation import wait_for_reply_completion

class ConversationMemoryTests(unittest.TestCase):
    def test_waiting_does_not_capture_whole_page_each_poll(self):
        page=Mock()
        completion=SimpleNamespace(failure=None,reply_completed=False)
        page.get_by_role.return_value.count.return_value=0
        page.get_by_role.return_value.filter.return_value.count.return_value=0
        with patch('executor.platforms.claude.conversation.time.monotonic', side_effect=[0,0,0,.2,.2,.4,.4,1]), patch('executor.platforms.claude.conversation.capture_accessibility_snapshot', return_value='final') as snapshot, patch('executor.platforms.claude.conversation.has_visible_element', return_value=False), patch('executor.platforms.claude.conversation.permission_pending', return_value=False), patch('executor.platforms.claude.conversation.current_reply_finished', return_value=False):
            ok, result=wait_for_reply_completion(page,completion=completion,timeout=1)
        self.assertFalse(ok)
        self.assertEqual(result,'final')
        self.assertEqual(snapshot.call_count,1)
