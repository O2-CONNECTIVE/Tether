import importlib.util
import contextlib
import io
import json
import os
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('consume_replay', SCRIPTS / 'check_mac_standby_consume.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class ConsumeReplayCheckerTests(unittest.TestCase):
    def events(self):
        events = [{'Action': 'pass', 'Test': name} for _ in range(tool.ITERATIONS)
                  for name in tool.TEST_NAMES]
        return events + [{'Action': 'pass', 'Package': tool.PACKAGE}]

    def report(self, events):
        return '\n'.join(json.dumps(event) for event in events)

    def test_complete_replay_has_positive_exact_counts(self):
        self.assertEqual(tool.validate_output(self.report(self.events())), 4 * tool.ITERATIONS)

    def test_failure_skip_missing_wrong_or_zero_tests_refuse(self):
        events = self.events()
        for case in ([], events[1:], events + [{'Action': 'skip'}],
                     events + [{'Action': 'fail'}], events + [{'Action': 'pass', 'Test': 'other'}]):
            with self.subTest(events=case), self.assertRaises(ValueError):
                tool.validate_output(self.report(case))

    def test_overlay_composition_never_normal_go_source(self):
        overlay = tool.overlay_files()['Replace']
        self.assertEqual(len(overlay), 4)
        self.assertTrue(all(not Path(target).exists() for target in overlay))
        self.assertTrue(all('testdata' in Path(source).parts and Path(source).is_file() for source in overlay.values()))

    def test_cache_paths_use_temporary_and_inherited_values(self):
        seen = []
        report = self.report(self.events())

        def command(args, env, cwd=None):
            seen.append(dict(env))
            return 'go1.27.3' if args[-2:] == ['env', 'GOVERSION'] else report

        with patch.object(tool.sys, 'platform', 'darwin'), \
             patch.object(tool.platform, 'machine', return_value='arm64'), \
             patch.object(tool.sys, 'argv', ['check_mac_standby_consume.py']), \
             patch.object(tool.shutil, 'which', return_value='/fixture/go'), \
             patch.dict(tool.os.environ, {'GOMODCACHE': '/fixture/module-cache'}, clear=False), \
             patch.object(tool, 'command', side_effect=command), \
             contextlib.redirect_stdout(io.StringIO()):
            tool.run()

        self.assertEqual(len(seen), 3)
        self.assertTrue(all(env['GOMODCACHE'] == '/fixture/module-cache' for env in seen))
        self.assertTrue(all(Path(env['GOCACHE']).name == 'cache' for env in seen[1:]))
        self.assertTrue(all('standby-consume-replay-' in env['GOCACHE'] for env in seen[1:]))
        self.assertFalse(hasattr(tool, 'GOCACHE'))
        self.assertFalse(hasattr(tool, 'GOMODCACHE'))

    def test_non_mac_refuses_before_compiler(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
