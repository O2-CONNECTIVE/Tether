"""Windows-only pre-native qualification: pinned real adapter ABI fixture must run."""
import argparse
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import unittest

REQUIRED = 'test_wry_pump_brackets.PumpBracketTests.test_exact_pump_held_brackets_returns_and_negative_controls'


def cases(suite):
    for item in suite:
        if isinstance(item, unittest.TestSuite): yield from cases(item)
        else: yield item


class ExecutedResult(unittest.TextTestResult):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.requiredStarted = 0
        self.requiredPassed = 0

    def startTest(self, test):
        if test.id() == REQUIRED: self.requiredStarted += 1
        super().startTest(test)

    def addSuccess(self, test):
        if test.id() == REQUIRED: self.requiredPassed += 1
        super().addSuccess(test)


def qualify(root):
    if sys.platform != 'win32' or platform.machine().lower() not in ('amd64', 'x86_64'):
        raise ValueError('WINDOWS_X64_REQUIRED')
    info = subprocess.run([os.environ['WRY_PROBE_RUSTC'], '-vV'], capture_output=True, timeout=15)
    if info.returncode or info.stdout.decode('utf-8').splitlines().count('host: x86_64-pc-windows-msvc') != 1:
        raise ValueError('WINDOWS_RUST_TARGET_REQUIRED')
    suite = unittest.defaultTestLoader.discover(str(root / 'scripts/tests'), pattern='test_wry*.py')
    selected = list(cases(suite))
    if sum(case.id() == REQUIRED for case in selected) != 1:
        raise ValueError('LAST_ERROR_CASE_REQUIRED')
    result = unittest.TextTestRunner(verbosity=2, resultclass=ExecutedResult).run(suite)
    if (not result.wasSuccessful() or result.testsRun != len(selected) or not selected
            or result.skipped or result.expectedFailures or result.unexpectedSuccesses
            or result.requiredStarted != 1 or result.requiredPassed != 1):
        raise ValueError('WINDOWS_FIXTURE_REJECTED')
    return {'schema': 1, 'qualification': 'windows-x64-pump-adapter-last-error',
            'testsRun': result.testsRun, 'lastErrorCaseExecuted': True,
            'firstUseTlsAndOriginalErrorConversion': True, 'guardRemovalNegativeControl': True,
            'nativeLaunches': 0, 'passed': True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', required=True, type=Path)
    args = parser.parse_args()
    try:
        value = qualify(args.root)
    except (OSError, ValueError, KeyError, UnicodeError, subprocess.SubprocessError):
        print('{"code":"WINDOWS_PUMP_QUALIFICATION_REJECTED"}')
        return 1
    print(json.dumps(value, sort_keys=True, separators=(',', ':')))
    return 0


if __name__ == '__main__': raise SystemExit(main())
