import importlib.util
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

SPEC = importlib.util.spec_from_file_location('enrollment_lookup', Path(__file__).resolve().parents[1] / 'check_mac_enrollment_lookup.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)


class EnrollmentLookupDriverTests(unittest.TestCase):
    def test_interrupt_awaits_the_same_driver_and_refuses_success(self):
        process = Mock(returncode=0)
        process.communicate.side_effect = [KeyboardInterrupt(), ('fixture', '')]
        with patch.object(tool.subprocess, 'Popen', return_value=process) as start:
            with self.assertRaises(ValueError):
                tool.command(['go', 'test'], {})
        self.assertEqual(start.call_count, 1)
        self.assertEqual(process.communicate.call_count, 2)
        process.kill.assert_not_called()
        process.terminate.assert_not_called()

    def test_failed_driver_never_becomes_fixture_success(self):
        process = Mock(returncode=1)
        process.communicate.return_value = ('sensitive fixture diagnostic', 'not exposed')
        with patch.object(tool.subprocess, 'Popen', return_value=process):
            with self.assertRaisesRegex(ValueError, '^fixture driver failed or interrupted$'):
                tool.command(['go', 'test'], {})

    def test_unsupported_host_is_a_failure_before_any_go_process(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool.subprocess, 'Popen') as start:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()
