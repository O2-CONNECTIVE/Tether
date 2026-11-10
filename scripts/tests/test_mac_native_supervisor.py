import importlib.util
from pathlib import Path
import sys
import subprocess
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('native_supervisor', SCRIPTS / 'check_mac_native_supervisor.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class NativeSupervisorReportTests(unittest.TestCase):
    def test_every_mode_requires_exact_complete_report(self):
        for mode, count in {'supervisor-normal': 10, **tool.CASES}.items():
            child = int(mode not in tool.NO_CHILD)
            unknown = int(mode not in tool.NO_CHILD and mode not in tool.TERMINAL)
            report = f'NATIVE_SUPERVISOR_FIXTURE mode={mode} passed={count} failed=0 childrenStarted={child} childrenWaited={child} open=0 unknown={unknown} producer=0\n'
            self.assertEqual(tool.validate_report(mode, report), count)
            for bad in ['', report + report, report + 'extra', report.replace(f'passed={count}', 'passed=0'),
                        report.replace('failed=0', 'failed=1'), report.replace(f'childrenStarted={child}', f'childrenStarted={child+1}'),
                        report.replace(f'childrenWaited={child}', f'childrenWaited={1-child}'), report.replace('open=0', 'open=1'),
                        report.replace(f'unknown={unknown}', f'unknown={1-unknown}'), report.replace('producer=0', 'producer=1')]:
                with self.subTest(mode=mode, output=bad), self.assertRaises(ValueError):
                    tool.validate_report(mode, bad)

    def test_startup_seal_is_executed_without_a_synthetic_child(self):
        self.assertEqual(tool.CASES['supervisor-startup-sealed'], 16)
        self.assertIn('supervisor-startup-sealed', tool.NO_CHILD)
        self.assertNotIn('supervisor-startup-sealed', tool.TERMINAL)

    def test_terminal_modes_are_explicit(self):
        self.assertEqual(tool.TERMINAL, {'supervisor-rejected', 'supervisor-confirmed', 'supervisor-drop'})

    def test_exit_two_without_startup_proof_stays_unknown(self):
        mode = 'supervisor-unproven-exit-two'
        report = f'NATIVE_SUPERVISOR_FIXTURE mode={mode} passed=17 failed=0 childrenStarted=1 childrenWaited=1 open=0 unknown=1 producer=0\n'
        self.assertEqual(tool.validate_report(mode, report), 17)
        with self.assertRaises(ValueError):
            tool.validate_report(mode, report.replace('unknown=1', 'unknown=0'))

    def test_unknown_mode_is_not_success(self):
        with self.assertRaises(ValueError):
            tool.validate_report('other', 'NATIVE_SUPERVISOR_FIXTURE mode=other passed=1 failed=0 childrenStarted=1 childrenWaited=1 open=0 unknown=1 producer=0')

    def test_dependencies_remain_in_existing_app_lock(self):
        tool.validate_dependencies()

    def test_non_mac_refuses_before_dependency_or_compiler(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start, patch.object(tool, 'validate_dependencies') as dependencies:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
        dependencies.assert_not_called()


class NativeSupervisorDiagnosticTests(unittest.TestCase):
    def test_failure_retains_only_fixed_mode_exit_and_valid_condition(self):
        for stderr, condition, child in [
            ('private/path\nnative supervisor condition 3\nNATIVE_SUPERVISOR_CHILD_EXIT 88\nsecret', 3, 88),
            ('\nnative supervisor condition 24\nNATIVE_SUPERVISOR_CHILD_EXIT 256\n', None, None),
            ('\nnative supervisor condition 999\n', None, None),
            ('private token or unrelated panic', None, None),
        ]:
            progress = {}
            result = subprocess.CompletedProcess([], 101, 'private payload', stderr)
            with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
                tool.execute_mode(Path('/private/fixture'), 'supervisor-discard-pending', progress)
            self.assertEqual(progress, {'phase': 'fixture', 'mode': 'supervisor-discard-pending',
                                       'returncode': 101, 'condition': condition, 'child_exit': child})

    def test_unknown_mode_is_rejected_before_execution_or_diagnostic_echo(self):
        progress = {}
        with patch.object(tool, 'completed') as execute, self.assertRaises(ValueError):
            tool.execute_mode(Path('/private/fixture'), 'private arbitrary input', progress)
        execute.assert_not_called()
        self.assertEqual(progress, {})

    def test_condition_is_bounded_by_selected_mode(self):
        progress = {}
        result = subprocess.CompletedProcess([], 101, '', '\nnative supervisor condition 18\n')
        with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
            tool.execute_mode(Path('/private/fixture'), 'supervisor-finite-malformed', progress)
        self.assertIsNone(progress['condition'])

    def test_successful_exit_with_bad_report_is_separate_failure(self):
        progress = {}
        result = subprocess.CompletedProcess([], 0, 'private malformed output', 'private stderr')
        with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
            tool.execute_mode(Path('/private/fixture'), 'supervisor-confirmed', progress)
        self.assertEqual(progress, {'phase': 'report', 'mode': 'supervisor-confirmed',
                                   'returncode': 0, 'condition': None, 'child_exit': None})

    def test_success_still_requires_complete_original_counts(self):
        progress = {}
        report = 'NATIVE_SUPERVISOR_FIXTURE mode=supervisor-confirmed passed=23 failed=0 childrenStarted=1 childrenWaited=1 open=0 unknown=0 producer=0\n'
        result = subprocess.CompletedProcess([], 0, report, 'ignored stderr')
        with patch.object(tool, 'completed', return_value=result):
            self.assertEqual(tool.execute_mode(Path('/private/fixture'), 'supervisor-confirmed', progress), 23)

    def test_build_failure_keeps_phase_and_exit_not_command_or_output(self):
        progress = {'phase': 'fixture-clippy', 'mode': None, 'returncode': None,
                    'condition': None, 'child_exit': None}
        result = subprocess.CompletedProcess([], 101, 'private source', 'private path/token')
        with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
            tool.build_command(['/private/compiler'], {}, progress)
        self.assertEqual(progress, {'phase': 'fixture-clippy', 'mode': None, 'returncode': 101,
                                   'condition': None, 'child_exit': None})
