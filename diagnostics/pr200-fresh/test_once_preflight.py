"""Synthetic preflight/cap fixtures. No Windows program is invoked by this file."""
import json
import contextlib
import io
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import phase2
import report
from test_phase2 import synthetic_preflight


class OncePreflightTests(unittest.TestCase):
    def test_one_trial_cap_rejects_second_and_third_before_any_launch(self):
        self.assertEqual(report.CAP, 1)
        with patch.object(phase2.sys, 'platform', 'win32'), patch.dict(os.environ, RUNNER_ENVIRONMENT='github-hosted'), \
             patch.object(phase2.subprocess, 'Popen') as launch, patch.object(report, 'identity') as identity:
            for index in [2, 3]:
                with self.assertRaises(ValueError): phase2.trial(Path('source'), Path('control'), Path('root'), index)
            launch.assert_not_called(); identity.assert_not_called()

    def test_preflight_pass_needs_runtime_exit_and_exact_five_check_marker(self):
        for code, stdout, stderr, expected in [(0, b'OBSERVER_LEDGER_SYNTHETIC_PASS checks=5\r\n', b'', True),
            (1, b'', b'PRIVATE_EXCEPTION', False), (0, b'PRIVATE_UNEXPECTED_OUTPUT', b'', False),
            (0, b'OBSERVER_LEDGER_SYNTHETIC_PASS checks=5\n', b'PRIVATE_STDERR', False)]:
            with tempfile.TemporaryDirectory() as folder:
                root = Path(folder)
                def run(args, **kwargs):
                    self.assertEqual(kwargs['timeout'], 60)
                    self.assertTrue(args[-1].endswith('observer-reasons.test.ps1'))
                    kwargs['stdout'].write(stdout); kwargs['stderr'].write(stderr)
                    return SimpleNamespace(returncode=code)
                with patch.object(phase2.sys, 'platform', 'win32'), patch.dict(os.environ, RUNNER_ENVIRONMENT='github-hosted'), \
                     patch.object(phase2.subprocess, 'run', side_effect=run) as command, contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(phase2.preflight(root, root), 0 if expected else 1)
                    self.assertEqual(command.call_count, 1)
                value = report.read(root / 'safe/powershell-preflight.json')
                self.assertEqual(value['passed'], expected)
                self.assertEqual(value['checksVerified'], 5 if expected else 0)
                self.assertTrue(value['runtimeExecuted'])
                self.assertNotIn('PRIVATE_', json.dumps(value))

    def test_timeout_or_unavailable_preflight_is_retained_and_never_native(self):
        for error, code, runtime in [(subprocess.TimeoutExpired('PRIVATE_COMMAND', 60), 'command-timeout', True),
                                     (OSError('PRIVATE_PATH'), 'command-unavailable', False)]:
            with tempfile.TemporaryDirectory() as folder:
                root = Path(folder)
                with patch.object(phase2.sys, 'platform', 'win32'), patch.dict(os.environ, RUNNER_ENVIRONMENT='github-hosted'), \
                     patch.object(phase2.subprocess, 'run', side_effect=error), patch.object(phase2.subprocess, 'Popen') as native, contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(phase2.preflight(root, root), 1)
                    value = report.read(root / 'safe/powershell-preflight.json')
                    self.assertEqual(value['code'], code)
                    self.assertEqual(value['runtimeExecuted'], runtime)
                    self.assertNotIn('PRIVATE_', json.dumps(value))
                    with self.assertRaises(ValueError): phase2.trial(root, root, root, 1)
                    native.assert_not_called()

    def test_preflight_record_cannot_call_static_or_missing_checks_a_runtime_pass(self):
        for change in [{'runtimeExecuted': False}, {'checksVerified': 0}, {'checksVerified': True},
                       {'exitCode': 1}, {'code': 'fixture-rejected'}, {'path': 'PRIVATE_PATH'}]:
            value = synthetic_preflight(); value.update(change)
            with self.assertRaises(ValueError): phase2.preflight_safe(value)


if __name__ == '__main__':
    unittest.main()
