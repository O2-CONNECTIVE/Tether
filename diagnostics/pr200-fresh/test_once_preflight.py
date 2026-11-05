"""Synthetic preflight/cap fixtures. All PowerShell calls mocked; no native invocation."""
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import phase2
import powershell_preflight as preflight
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

    def execute_mock(self, root, failed_index=None, output=b'PRIVATE_OUTPUT', stderr=b'', exit_code=0, error=None):
        calls = []
        def run(args, **kwargs):
            index = len(calls); calls.append(Path(args[-1]).name)
            self.assertEqual(kwargs['timeout'], 60)
            self.assertEqual(calls[-1], preflight.SCRIPTS[index])
            if index == failed_index and error is not None: raise error
            kwargs['stdout'].write(output if index == failed_index else (preflight.MARKERS[index] + '\r\n').encode())
            kwargs['stderr'].write(stderr if index == failed_index else b'')
            return SimpleNamespace(returncode=exit_code if index == failed_index else 0)
        with patch.object(phase2.sys, 'platform', 'win32'), patch.dict(os.environ, RUNNER_ENVIRONMENT='github-hosted'), \
             patch.object(phase2.subprocess, 'run', side_effect=run), contextlib.redirect_stdout(io.StringIO()):
            code = phase2.preflight(root, root)
        value = report.read(root / 'safe/powershell-preflight.json')
        self.assertNotIn('PRIVATE_', json.dumps(value))
        return code, value, calls

    def test_preflight_pass_requires_new_parse_and_both_changed_fixture_executions(self):
        with tempfile.TemporaryDirectory() as folder:
            code, value, calls = self.execute_mock(Path(folder))
            self.assertEqual(code, 0); self.assertEqual(calls, list(preflight.SCRIPTS))
            self.assertEqual(value, synthetic_preflight())
            self.assertTrue(preflight.current_passed(value))

    def test_each_failed_stage_stops_before_next_stage_and_native(self):
        for index in range(3):
            for stdout, stderr, code in [(b'PRIVATE_OUTPUT', b'', 0),
                ((preflight.MARKERS[index] + '\n').encode(), b'PRIVATE_STDERR', 0),
                ((preflight.MARKERS[index] + '\n').encode(), b'', 1), (b'x' * 4097, b'', 0)]:
                with tempfile.TemporaryDirectory() as folder:
                    root = Path(folder)
                    result, value, calls = self.execute_mock(root, index, stdout, stderr, code)
                    self.assertEqual(result, 1); self.assertEqual(len(calls), index + 1)
                    self.assertFalse(value['passed']); self.assertEqual(value['fixturesVerified'], max(0, index - 1))
                    self.assertEqual(value['stages'][preflight.STAGES[index]]['code'], 'fixture-rejected')
                    self.assertTrue(all(value['stages'][key]['code'] == 'not-run' for key in preflight.STAGES[index + 1:]))
                    with patch.object(phase2.sys, 'platform', 'win32'), patch.dict(os.environ, RUNNER_ENVIRONMENT='github-hosted'), \
                         patch.object(phase2.subprocess, 'Popen') as native, patch.object(report, 'identity') as identity:
                        with self.assertRaises(ValueError): phase2.trial(root, root, root, 1)
                        native.assert_not_called(); identity.assert_not_called()

    def test_timeout_or_unavailable_at_each_stage_retains_safe_failure_and_stops(self):
        for index in range(3):
            for error, code, runtime in [(subprocess.TimeoutExpired('PRIVATE_COMMAND', 60), 'command-timeout', True),
                                         (OSError('PRIVATE_PATH'), 'command-unavailable', False)]:
                with tempfile.TemporaryDirectory() as folder:
                    result, value, calls = self.execute_mock(Path(folder), index, error=error)
                    self.assertEqual(result, 1); self.assertEqual(len(calls), index + 1)
                    stage = value['stages'][preflight.STAGES[index]]
                    self.assertEqual(stage['code'], code); self.assertEqual(stage['runtimeExecuted'], runtime)

    def test_legacy_five_check_record_is_readable_but_never_authorizes_current_trial(self):
        value = {'schema': 1, 'kind': 'synthetic-powershell-ledger', 'runtimeExecuted': True,
            'checksExpected': 5, 'checksVerified': 5, 'exitCode': 0, 'passed': True, 'code': 'pass'}
        self.assertEqual(phase2.preflight_safe(value), value)
        self.assertFalse(preflight.current_passed(value))
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder); report.write(root / 'safe/powershell-preflight.json', value)
            with patch.object(phase2.sys, 'platform', 'win32'), patch.dict(os.environ, RUNNER_ENVIRONMENT='github-hosted'), \
                 patch.object(phase2.subprocess, 'Popen') as native, patch.object(report, 'identity') as identity:
                with self.assertRaises(ValueError): phase2.trial(root, root, root, 1)
                native.assert_not_called(); identity.assert_not_called()

    def test_preflight_unknown_fields_inconsistent_counts_static_or_skipped_cannot_pass(self):
        for mutate in [lambda x: x.update(fixturesVerified=1), lambda x: x.update(parseFilesVerified=0),
            lambda x: x.update(fixturesVerified=True), lambda x: x.update(path='PRIVATE_PATH'),
            lambda x: x['stages']['detail'].update(runtimeExecuted=False),
            lambda x: x['stages']['detail'].update(exitCode=1),
            lambda x: x['stages']['parse'].update(code='not-run', runtimeExecuted=False, exitCode=None, passed=False),
            lambda x: x['stages']['ledger'].update(exception='PRIVATE_EXCEPTION')]:
            value = copy.deepcopy(synthetic_preflight()); mutate(value)
            with self.assertRaises(ValueError): phase2.preflight_safe(value)


if __name__ == '__main__':
    unittest.main()
