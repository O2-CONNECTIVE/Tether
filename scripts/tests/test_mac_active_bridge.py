import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('active_bridge', SCRIPTS / 'check_mac_active_bridge.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class ActiveBridgeReportTests(unittest.TestCase):
    def test_all_complete_reports_including_retained_unknown(self):
        for mode, passed in {'normal': 4, **tool.CASES}.items():
            child = int(mode not in tool.NO_CHILD)
            line = f'NATIVE_ACTIVE_BRIDGE mode={mode} passed={passed} failed=0 childrenStarted={child} childrenWaited={child} open=0 unknown={child} producer=0\n'
            self.assertEqual(tool.validate_report(mode, line), passed)

    def test_zero_failed_uncollected_or_replacement_reports_reject(self):
        report = 'NATIVE_ACTIVE_BRIDGE mode=echo passed=32 failed=0 childrenStarted=1 childrenWaited=1 open=0 unknown=1 producer=0\n'
        for output in ['', report + report, report + 'extra', report.replace('passed=32', 'passed=0'),
                       report.replace('failed=0', 'failed=1'), report.replace('childrenStarted=1', 'childrenStarted=2'),
                       report.replace('childrenWaited=1', 'childrenWaited=0'), report.replace('open=0', 'open=1'),
                       report.replace('unknown=1', 'unknown=0'), report.replace('producer=0', 'producer=1'),
                       report.replace('mode=echo', 'mode=other')]:
            with self.subTest(output=output), self.assertRaises(ValueError):
                tool.validate_report('echo', output)
        with self.assertRaises(ValueError):
            tool.validate_report('other', report.replace('mode=echo', 'mode=other'))

    def test_non_mac_refuses_before_compiler_or_child(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
