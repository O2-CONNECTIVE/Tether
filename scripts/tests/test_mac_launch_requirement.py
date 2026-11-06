import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('launch_requirement', SCRIPTS / 'check_mac_launch_requirement.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class NativeLaunchRequirementReportTests(unittest.TestCase):
    def test_complete_inert_fixture_has_positive_result(self):
        self.assertEqual(tool.validate_report('NATIVE_REQUIREMENT_FIXTURE passed=35 failed=0 launched=0\n'), 35)

    def test_missing_partial_failed_or_launched_results_are_not_success(self):
        for output in ['', 'NATIVE_REQUIREMENT_UNEXECUTED unsupportedOS',
                       'NATIVE_REQUIREMENT_FIXTURE passed=0 failed=0 launched=0',
                       'NATIVE_REQUIREMENT_FIXTURE passed=34 failed=0 launched=0',
                       'NATIVE_REQUIREMENT_FIXTURE passed=35 failed=1 launched=0',
                       'NATIVE_REQUIREMENT_FIXTURE passed=35 failed=0 launched=1',
                       'NATIVE_REQUIREMENT_FIXTURE passed=35 failed=0 launched=0\nextra']:
            with self.subTest(output=output), self.assertRaises(ValueError):
                tool.validate_report(output)

    def test_missing_duplicate_or_strong_framework_link_is_refused(self):
        weak = 'Load command 1\n cmd LC_LOAD_WEAK_DYLIB\n name /System/Library/Frameworks/LightweightCodeRequirements.framework/Versions/A/LightweightCodeRequirements (offset 24)\n'
        tool.validate_weak_link(weak)
        for output in ['', weak + weak, weak.replace('LC_LOAD_WEAK_DYLIB', 'LC_LOAD_DYLIB')]:
            with self.subTest(output=output), self.assertRaises(ValueError):
                tool.validate_weak_link(output)

    def test_non_mac_refuses_before_any_compiler(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
