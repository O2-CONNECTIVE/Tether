import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('native_pump', SCRIPTS / 'check_mac_native_pump.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class NativePumpCheckerTests(unittest.TestCase):
    def test_every_mode_requires_complete_positive_conditions(self):
        for mode, count in {'normal': 1, **tool.CASES}.items():
            report = f'NATIVE_PUMP_FIXTURE mode={mode} passed={count} failed=0 launched=0\n'
            self.assertEqual(tool.validate_report(mode, report), count)
            for bad in ['', report + 'extra', report.replace(f'passed={count}', 'passed=0'),
                        report.replace('failed=0', 'failed=1'), report.replace('launched=0', 'launched=1')]:
                with self.subTest(mode=mode, output=bad), self.assertRaises(ValueError):
                    tool.validate_report(mode, bad)

    def test_fixture_dependencies_are_in_existing_app_lock(self):
        tool.validate_dependencies()

    def test_unknown_mode_is_not_success(self):
        with self.assertRaises(ValueError):
            tool.validate_report('other', 'NATIVE_PUMP_FIXTURE mode=other passed=1 failed=0 launched=0')

    def test_non_mac_refuses_before_dependency_or_compiler(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start, patch.object(tool, 'validate_dependencies') as dependencies:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
        dependencies.assert_not_called()
