import importlib.util
from pathlib import Path
import sys
import subprocess
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('native_session', SCRIPTS / 'check_mac_native_session.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class NativeSessionReportTests(unittest.TestCase):
    def test_every_mode_requires_exact_complete_report(self):
        for mode, count in {'session-normal': 8, **tool.CASES}.items():
            child = int(mode not in tool.NO_CHILD)
            unknown = int(mode not in tool.NO_CHILD and mode not in tool.TERMINAL)
            report = f'NATIVE_SESSION_FIXTURE mode={mode} passed={count} failed=0 childrenStarted={child} childrenWaited={child} open=0 nativeUnknown={unknown} producer=0\n'
            self.assertEqual(tool.validate_report(mode, report), count)
            for bad in ['', report + report, report + 'extra', report.replace(f'passed={count}', 'passed=0'),
                        report.replace('failed=0', 'failed=1'), report.replace(f'childrenStarted={child}', f'childrenStarted={child+1}'),
                        report.replace(f'childrenWaited={child}', f'childrenWaited={1-child}'), report.replace('open=0', 'open=1'),
                        report.replace(f'nativeUnknown={unknown}', f'nativeUnknown={1-unknown}'), report.replace('producer=0', 'producer=1')]:
                with self.subTest(mode=mode, output=bad), self.assertRaises(ValueError):
                    tool.validate_report(mode, bad)

    def test_terminal_modes_are_explicit(self):
        self.assertEqual(tool.TERMINAL, {'session-confirmed', 'session-rejected', 'session-close-idle', 'session-close-pending', 'session-drop'})

    def test_unknown_mode_is_not_success(self):
        with self.assertRaises(ValueError):
            tool.validate_report('other', 'NATIVE_SESSION_FIXTURE mode=other passed=1 failed=0 childrenStarted=1 childrenWaited=1 open=0 nativeUnknown=1 producer=0')

    def test_dependencies_remain_in_existing_app_lock(self):
        tool.validate_dependencies()

    def test_non_mac_refuses_before_dependency_or_compiler(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start, patch.object(tool, 'validate_dependencies') as dependencies:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
        dependencies.assert_not_called()

    def test_borrow_refusal_requires_the_specific_real_error(self):
        specific = 'error[E0505]: cannot move out of `session` because it is borrowed'
        tool.validate_borrow_refusal(1, specific)
        for code, log in [(0, specific), (1, ''), (1, 'error[E0432]: missing import'),
                          (1, specific + '\nerror[E0432]: missing import'),
                          (1, 'error[E0505]: unrelated borrow')]:
            with self.subTest(code=code, log=log), self.assertRaises(ValueError):
                tool.validate_borrow_refusal(code, log)


class NativeSessionFailureEvidenceTests(unittest.TestCase):
    def test_fixture_failure_keeps_only_mode_exit_and_bounded_condition(self):
        for stderr, condition in [('private/path\nsession condition 6\nsecret', 6),
                                  ('\nsession condition 27\n', None),
                                  ('\nsession condition 999\n', None),
                                  ('private token or arbitrary exception', None)]:
            progress = {}
            result = subprocess.CompletedProcess([], 101, 'private payload', stderr)
            with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
                tool.execute_mode(Path('/private/fixture'), 'session-confirmed', progress)
            self.assertEqual(progress, {'phase': 'fixture', 'mode': 'session-confirmed',
                                        'returncode': 101, 'condition': condition})

    def test_malformed_success_is_report_failure_not_fixture_success(self):
        progress = {}
        result = subprocess.CompletedProcess([], 0, 'private malformed payload', 'private stderr')
        with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
            tool.execute_mode(Path('/private/fixture'), 'session-confirmed', progress)
        self.assertEqual(progress, {'phase': 'report', 'mode': 'session-confirmed',
                                   'returncode': 0, 'condition': None})

    def test_completed_report_still_requires_original_counts(self):
        progress = {}
        report = 'NATIVE_SESSION_FIXTURE mode=session-confirmed passed=26 failed=0 childrenStarted=1 childrenWaited=1 open=0 nativeUnknown=0 producer=0\n'
        result = subprocess.CompletedProcess([], 0, report, 'ignored private stderr')
        with patch.object(tool, 'completed', return_value=result):
            self.assertEqual(tool.execute_mode(Path('/private/fixture'), 'session-confirmed', progress), 26)


class GuiStateReportTests(unittest.TestCase):
    def test_exact_owner_reports_only(self):
        for mode, count in tool.GUI_CASES.items():
            child = int(mode != 'gui-state-normal')
            unknown = int(mode == 'gui-state-unknown')
            report = f'GUI_STATE_FIXTURE mode={mode} passed={count} failed=0 childrenStarted={child} childrenWaited={child} open=0 nativeUnknown={unknown} producer=0\n'
            self.assertEqual(tool.validate_gui_report(mode, report), count)
            for bad in ['', report + report, report + 'raw',
                        report.replace(f'passed={count}', 'passed=0'),
                        report.replace('failed=0', 'failed=1'),
                        report.replace(f'childrenWaited={child}', f'childrenWaited={1-child}'),
                        report.replace(f'nativeUnknown={unknown}', f'nativeUnknown={1-unknown}'),
                        report.replace('open=0', 'open=1'),
                        report.replace('producer=0', 'producer=1')]:
                with self.assertRaises(ValueError):
                    tool.validate_gui_report(mode, bad)
        with self.assertRaises(ValueError):
            tool.validate_gui_report('other', '')

    def test_failed_gui_state_never_exposes_raw_output(self):
        progress = {}
        result = subprocess.CompletedProcess([], 101, 'private payload', 'private path')
        with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
            tool.execute_gui_mode(Path('/private/fixture'), 'gui-state-unknown', progress)
        self.assertEqual(progress, {'phase':'gui-state', 'mode':'gui-state-unknown',
                                    'returncode':101, 'condition':None})
