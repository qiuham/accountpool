import unittest
from executor.contracts.job import JobSpec
from executor.platforms.claude.adapter import ClaudePlatform

class ScheduledMessageTest(unittest.TestCase):
    def test_single_message_and_prompt_are_passed_as_arguments(self):
        prompt = '你好，请回复一句话。'
        spec = JobSpec(platform='claude', account='fixture', steps=['warmup'], options={'prompt': prompt})
        args = ClaudePlatform()._build_step_arguments('warmup', spec)
        self.assertEqual(args[args.index('--turns') + 1], '1')
        self.assertEqual(args[args.index('--prompt') + 1], prompt)
