import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('active_io', SCRIPTS / 'check_mac_active_io.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class ActiveIOReportTests(unittest.TestCase):
    def test_complete_positive_and_unknown_reports(self):
        for mode, (passed, children, unknown) in tool.CASES.items():
            report = f'NATIVE_ACTIVE_IO mode={mode} passed={passed} failed=0 childrenStarted={children} childrenWaited={children} open=0 unknown={unknown} producer=0\n'
            self.assertEqual(tool.validate_report(mode, report), passed)

    def test_failed_uncollected_or_recertified_reports_reject(self):
        for mode, (passed, children, unknown) in tool.CASES.items():
            report = f'NATIVE_ACTIVE_IO mode={mode} passed={passed} failed=0 childrenStarted={children} childrenWaited={children} open=0 unknown={unknown} producer=0\n'
            bad = ['', report + report, report + 'extra', report.replace(f'passed={passed}', 'passed=0'),
                   report.replace('failed=0', 'failed=1'), report.replace('open=0', 'open=1'),
                   report.replace(f'unknown={unknown}', f'unknown={1-unknown}'),
                   report.replace('producer=0', 'producer=1'), report.replace(f'mode={mode}', 'mode=other')]
            if children:
                bad += [report.replace('childrenWaited=1', 'childrenWaited=0'),
                        report.replace('childrenStarted=1', 'childrenStarted=2')]
            for output in bad:
                with self.subTest(mode=mode, output=output), self.assertRaises(ValueError):
                    tool.validate_report(mode, output)

    def test_non_mac_refuses_before_compiler(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
