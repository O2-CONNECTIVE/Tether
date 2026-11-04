"""Caller-owned pipes and failure evidence; no real children or windows."""
from contextlib import redirect_stdout
import importlib.util
import io
import fcntl
import os
import stat
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
    def test_public_fixture_caller_prepares_dedicated_nonblocking_output(self):
        read_fd, write_fd = OUTPUT.output_pipe()
        try:
            self.assertNotEqual(read_fd, write_fd)
            self.assertTrue(stat.S_ISFIFO(os.fstat(write_fd).st_mode))
            flags = fcntl.fcntl(write_fd, fcntl.F_GETFL)
            self.assertEqual(flags & os.O_ACCMODE, os.O_WRONLY)
            self.assertTrue(flags & os.O_NONBLOCK)
            self.assertEqual(fcntl.fcntl(write_fd, 74), 1)
        finally:
            os.close(read_fd)
            os.close(write_fd)

    def test_reader_loss_timeout_retains_native_pid_and_failure(self):
        capture = io.StringIO()
        with redirect_stdout(capture), self.assertRaisesRegex(RuntimeError, 'owned child unreaped: 456'):
            OUTPUT.wait_owned(StuckChild(), 1, 'reader-loss')
        self.assertEqual(json.loads(capture.getvalue()), dict(case='reader-loss',
                         result='timeout_child_unconfirmed', owned_pid=456, reaped=False))


if __name__ == '__main__':
    unittest.main()
