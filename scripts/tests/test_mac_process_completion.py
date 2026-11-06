import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('process_completion', SCRIPTS / 'check_mac_process_completion.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class CompletionReportTests(unittest.TestCase):
    def test_exact_positive_case_reports(self):
        for mode, (passed, children) in tool.CASES.items():
            line = f'NATIVE_PROCESS_COMPLETION mode={mode} passed={passed} failed=0 childrenStarted={children} childrenWaited={children} producer=0\n'
            self.assertEqual(tool.validate_report(mode, line), passed)

    def test_missing_failed_uncollected_or_producer_reports_reject(self):
        valid = 'NATIVE_PROCESS_COMPLETION mode=exit-zero passed=6 failed=0 childrenStarted=1 childrenWaited=1 producer=0\n'
        for output in ['', valid + valid, valid + 'extra', valid.replace('passed=6', 'passed=0'),
                       valid.replace('failed=0', 'failed=1'), valid.replace('childrenStarted=1', 'childrenStarted=2'),
                       valid.replace('childrenWaited=1', 'childrenWaited=0'), valid.replace('producer=0', 'producer=1'),
                       valid.replace('exit-zero', 'unknown')]:
            with self.subTest(output=output), self.assertRaises(ValueError):
                tool.validate_report('exit-zero', output)

    def test_non_mac_refuses_before_any_compiler(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
