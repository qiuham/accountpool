import importlib
import unittest
from executor.platforms.registry import load_platform

class ExecutorStructureTests(unittest.TestCase):
    def test_registry_loads_explicit_adapter(self):
        adapter = load_platform("claude")
        self.assertEqual(type(adapter).__module__, "executor.platforms.claude.adapter")
        self.assertEqual(adapter.supported_steps, ("register", "onboard", "warmup", "session"))

    def test_domain_and_runtime_modules_import(self):
        for name in ("registration", "onboarding", "conversation", "page_diagnostics"):
            with self.subTest(name=name):
                importlib.import_module("executor.platforms.claude." + name)
        for name in ("config", "fingerprint", "font_subset", "browser_services"):
            importlib.import_module("executor.runtime." + name)
