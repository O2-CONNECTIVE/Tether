import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('preparation_ffi', SCRIPTS / 'check_mac_preparation_ffi.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class PreparationFFIReportTests(unittest.TestCase):
    def test_all_modes_require_their_complete_positive_result(self):
        for mode, count in tool.CASES.items():
            with self.subTest(mode=mode):
                result = f'NATIVE_PREPARATION_FFI mode={mode} passed={count} failed=0 launched=0\n'
                self.assertEqual(tool.validate_report(mode, result), count)
                for bad in ['', result + 'extra', result.replace(f'passed={count}', 'passed=0'),
                            result.replace('failed=0', 'failed=1'), result.replace('launched=0', 'launched=1'),
                            result.replace(f'mode={mode}', 'mode=other')]:
                    with self.assertRaises(ValueError):
                        tool.validate_report(mode, bad)

    def test_unknown_mode_is_not_success(self):
        with self.assertRaises(ValueError):
            tool.validate_report('other', 'NATIVE_PREPARATION_FFI mode=other passed=1 failed=0 launched=0')

    def test_non_mac_refuses_before_compiling(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
