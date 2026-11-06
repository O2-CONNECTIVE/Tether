import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('native_outer', SCRIPTS / 'check_mac_native_outer.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class NativeOuterReportTests(unittest.TestCase):
    def test_every_mode_requires_exact_complete_report(self):
        for mode, count in {'outer-normal': 8, **tool.CASES}.items():
            child = int(mode not in tool.NO_CHILD)
            report = f'NATIVE_OUTER_FIXTURE mode={mode} passed={count} failed=0 childrenStarted={child} childrenWaited={child} open=0 nativeUnknown={child} producer=0\n'
            self.assertEqual(tool.validate_report(mode, report), count)
            for bad in ['', report + report, report + 'extra', report.replace(f'passed={count}', 'passed=0'),
                        report.replace('failed=0', 'failed=1'), report.replace(f'childrenStarted={child}', f'childrenStarted={child+1}'),
                        report.replace(f'childrenWaited={child}', f'childrenWaited={1-child}'), report.replace('open=0', 'open=1'),
                        report.replace(f'nativeUnknown={child}', f'nativeUnknown={1-child}'), report.replace('producer=0', 'producer=1')]:
                with self.subTest(mode=mode, output=bad), self.assertRaises(ValueError):
                    tool.validate_report(mode, bad)

    def test_unknown_mode_is_not_success(self):
        with self.assertRaises(ValueError):
            tool.validate_report('other', 'NATIVE_OUTER_FIXTURE mode=other passed=1 failed=0 childrenStarted=1 childrenWaited=1 open=0 nativeUnknown=1 producer=0')

    def test_dependencies_remain_in_existing_app_lock(self):
        tool.validate_dependencies()

    def test_non_mac_refuses_before_dependency_or_compiler(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start, patch.object(tool, 'validate_dependencies') as dependencies:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
        dependencies.assert_not_called()
