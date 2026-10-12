import unittest
from pathlib import Path
from unittest.mock import patch
from executor.runtime import config
class ProxyRequiredTests(unittest.TestCase):
    def test_browser_without_task_proxy_is_blocked(self):
        with patch.dict("os.environ", {"ACCOUNTPOOL_REQUIRE_PROXY":"true"}), patch.object(config, "PROXY", ""):
            with self.assertRaisesRegex(ValueError, "代理"):
                config.camoufox_kwargs(None, Path("/fixture"))
    def test_browser_uses_explicit_task_proxy(self):
        with patch.object(config, "PROXY", "http://127.0.0.1:9999"):
            self.assertEqual(config.camoufox_kwargs(None, Path("/fixture"))["proxy"], {"server":"http://127.0.0.1:9999"})

    def test_adapter_ignores_inherited_proxy_without_task_assignment(self):
        from executor.contracts.job import JobSpec
        from executor.platforms.claude.adapter import ClaudePlatform
        with patch.dict("os.environ", {"ACCOUNTPOOL_PROXY":"http://127.0.0.1:9999","ACCOUNTPOOL_REQUIRE_PROXY":"true"}):
            with patch.object(ClaudePlatform, "_execute_step") as execute:
                result=ClaudePlatform().run(JobSpec(platform="claude",account="fixture",steps=["register"]))
                self.assertEqual(result.reason, "proxy-required")
                execute.assert_not_called()

    def test_default_browser_can_be_direct(self):
        with patch.dict("os.environ", {"ACCOUNTPOOL_REQUIRE_PROXY":""}), patch.object(config, "PROXY", ""):
            self.assertNotIn("proxy", config.camoufox_kwargs(None, Path("/fixture")))
