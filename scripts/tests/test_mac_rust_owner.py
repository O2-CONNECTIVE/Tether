import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('rust_owner', SCRIPTS / 'check_mac_rust_owner.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class NativeRustOwnerCheckerTests(unittest.TestCase):
    def test_every_report_requires_complete_positive_conditions(self):
        for mode, count in {'normal': 1, **tool.CASES}.items():
            report = f'NATIVE_RUST_OWNER mode={mode} passed={count} failed=0 launched=0\n'
            self.assertEqual(tool.validate_report(mode, report), count)
            for bad in ['', report + 'extra', report.replace(f'passed={count}', 'passed=0'),
                        report.replace('failed=0', 'failed=1'), report.replace('launched=0', 'launched=1')]:
                with self.subTest(mode=mode, output=bad), self.assertRaises(ValueError):
                    tool.validate_report(mode, bad)

    def test_unexported_and_fixture_contaminated_libraries_are_refused(self):
        normal = '\n'.join(tool.NORMAL_EXPORTS)
        tool.validate_exports(normal, False)
        tool.validate_exports(normal + '\n' + '\n'.join(tool.FIXTURE_EXPORTS), True)
        for output in ['', normal.replace('_rh_resident_preparation_create', '_other'),
                       normal + '\n_rh_fixture_preparation_create']:
            with self.subTest(output=output), self.assertRaises(ValueError):
                tool.validate_exports(output, False)

    def test_active_exports_are_observations_without_normal_fixture_creation(self):
        self.assertEqual(len(tool.NORMAL_EXPORTS), 11)
        self.assertEqual(len(tool.FIXTURE_EXPORTS), 15)
        self.assertTrue({'_rh_resident_preparation_exit', '_rh_resident_preparation_retire_active_io',
                         '_rh_resident_preparation_abandon_active',
                         '_rh_resident_preparation_request_active_close',
                         '_rh_resident_preparation_discard_observation'} <= tool.NORMAL_EXPORTS)
        self.assertNotIn('_rh_fixture_preparation_create_active', tool.NORMAL_EXPORTS)
        self.assertIn('_rh_fixture_preparation_create_active', tool.FIXTURE_EXPORTS)

    def test_compile_failure_must_be_the_specific_trait_rejection(self):
        diagnostic = {'level': 'error', 'code': {'code': 'E0277'}, 'message': 'Rc<()> cannot be sent between threads safely'}
        result = subprocess.CompletedProcess([], 1, '', json.dumps(diagnostic))
        tool.validate_rejection(result, 'E0277', 'cannot be sent')
        for result in [subprocess.CompletedProcess([], 0, '', json.dumps(diagnostic)),
                       subprocess.CompletedProcess([], 1, '', ''),
                       subprocess.CompletedProcess([], 1, '', json.dumps({**diagnostic, 'code': {'code': 'E0463'}})),
                       subprocess.CompletedProcess([], 1, '', json.dumps({**diagnostic, 'message': 'unrelated failure'}))]:
            with self.assertRaises(ValueError):
                tool.validate_rejection(result, 'E0277', 'cannot be sent')

    def test_unknown_mode_is_not_success(self):
        with self.assertRaises(ValueError):
            tool.validate_report('other', 'NATIVE_RUST_OWNER mode=other passed=1 failed=0 launched=0')

    def test_missing_io_api_is_not_a_valid_borrow_rejection(self):
        for code, message in [('E0499', 'more than once'), ('E0505', 'because it is borrowed')]:
            entry = {'level': 'error', 'code': {'code': code}, 'message': message}
            tool.validate_rejection(subprocess.CompletedProcess([], 1, '', json.dumps(entry)), code, message)
            missing = {**entry, 'code': {'code': 'E0599'}, 'message': 'no method named io'}
            with self.assertRaises(ValueError):
                tool.validate_rejection(subprocess.CompletedProcess([], 1, '', json.dumps(missing)), code, message)

    def test_non_mac_refuses_before_any_compiler(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
