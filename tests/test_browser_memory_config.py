import unittest
from pathlib import Path
from camoufox.addons import DefaultAddons
from executor.runtime.config import NO_DIALOG_PREFS, camoufox_kwargs

class BrowserMemoryConfigTests(unittest.TestCase):
    def test_launch_uses_bounded_caches_and_no_prelaunch(self):
        prefs=camoufox_kwargs(None, Path('/fixture'))['firefox_user_prefs']
        self.assertFalse(prefs['dom.ipc.processPrelaunch.enabled'])
        self.assertEqual(prefs['dom.ipc.processCount'], 2)
        self.assertEqual(prefs['dom.ipc.processCount.webIsolated'], 1)
        self.assertEqual(prefs['browser.sessionhistory.max_total_viewers'], 0)
        self.assertEqual(prefs['browser.cache.memory.capacity'], 32768)
        self.assertEqual(prefs['image.cache.size'], 16777216)
        self.assertNotIn('fission.autostart', prefs)
        self.assertNotIn('security.sandbox.content.level', prefs)

    def test_adblock_extension_is_not_loaded(self):
        self.assertEqual(camoufox_kwargs(None, Path('/fixture'))['exclude_addons'], [DefaultAddons.UBO])

    def test_launch_preferences_are_not_shared_mutable_state(self):
        prefs=camoufox_kwargs(None, Path('/fixture'))['firefox_user_prefs']
        prefs['test-mutation']=True
        self.assertNotIn('test-mutation', NO_DIALOG_PREFS)
