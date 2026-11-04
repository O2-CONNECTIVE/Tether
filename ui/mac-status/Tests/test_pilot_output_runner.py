"""Failure evidence only; no real children, pipes, or windows."""
from contextlib import redirect_stdout
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import unittest

SPEC = importlib.util.spec_from_file_location('pilot_output', Path(__file__).with_name('check_pilot_output.py'))
OUTPUT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(OUTPUT)


class StuckChild:
    pid = 456

    def wait(self, timeout):
        raise subprocess.TimeoutExpired('synthetic-native', timeout)


class OutputRunnerTests(unittest.TestCase):
    def test_reader_loss_timeout_retains_native_pid_and_failure(self):
        capture = io.StringIO()
        with redirect_stdout(capture), self.assertRaisesRegex(RuntimeError, 'owned child unreaped: 456'):
            OUTPUT.wait_owned(StuckChild(), 1, 'reader-loss')
        self.assertEqual(json.loads(capture.getvalue()), dict(case='reader-loss',
                         result='timeout_child_unconfirmed', owned_pid=456, reaped=False))


if __name__ == '__main__':
    unittest.main()
