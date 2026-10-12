"""每个场景独立 Camoufox/Firefox 实例，匹配执行侧的一次调用一次浏览器。"""
import unittest
from camoufox.pkgman import get_path
from camoufox.sync_api import Camoufox
from executor.runtime.config import NO_DIALOG_PREFS


class CamoufoxBrowserTests(unittest.TestCase):
    def setUp(self):
        manager = Camoufox(headless=True, geoip=False,
                           executable_path=str(get_path('camoufox')),
                           firefox_user_prefs=NO_DIALOG_PREFS)
        self.browser = manager.__enter__()
        self.addCleanup(manager.__exit__, None, None, None)
