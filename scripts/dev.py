"""Run the published Python checks without third-party dependencies."""

import argparse
import os
from pathlib import Path
import platform
import subprocess
import sys
import tokenize
import unittest


ROOT = Path(__file__).resolve().parents[1]
CI_PYTHON = '3.13.17'


class TestResult(unittest.TextTestResult):
    passed = 0

    def addSuccess(self, test):
        self.passed += 1
        super().addSuccess(test)


def python_files(directory):
    files = sorted((ROOT / directory).rglob('*.py'))
    if not files:
        raise ValueError(f'Missing or empty Python target: {directory}/')
    return files


def run_tests():
    sys.path.insert(0, str(ROOT / 'src'))
    suite = unittest.defaultTestLoader.discover(str(ROOT / 'tests'))
    if suite.countTestCases() == 0:
        raise ValueError('No tests discovered in tests/')
    result = unittest.TextTestRunner(verbosity=2, resultclass=TestResult).run(suite)
    skipped = len(result.skipped)
    executed = result.testsRun - skipped
    expected_failures = len(result.expectedFailures)
    unexpected_successes = len(result.unexpectedSuccesses)
    print(
        f'Tests: total={result.testsRun}, executed={executed}, passed={result.passed}, '
        f'skipped={skipped}, failures={len(result.failures)}, '
        f'errors={len(result.errors)}, expected_failures={expected_failures}, '
        f'unexpected_successes={unexpected_successes}',
        flush=True,
    )
    return result.wasSuccessful() and executed > 0


def check_syntax():
    files = []
    for directory in ('src', 'tests', 'scripts'):
        files.extend(python_files(directory))
    for path in files:
        with tokenize.open(path) as source:
            compile(source.read(), str(path.relative_to(ROOT)), 'exec')
    print(f'Syntax: {len(files)} Python files compiled (no bytecode written)', flush=True)
    return True


def check_tokens():
    checker = ROOT / 'ui/tauri/design/check_tokens.py'
    if not checker.is_file():
        raise ValueError('Missing token checker: ui/tauri/design/check_tokens.py')
    # The existing checker uses assertions; ignore PYTHONOPTIMIZE from the shell.
    return subprocess.run([sys.executable, '-I', str(checker)]).returncode == 0


def check_source_sizes():
    if sys.version_info < (3, 13):
        raise ValueError('Source-size checks require Python 3.13 or newer')
    checker = ROOT / 'scripts/check_source_sizes.py'
    if not checker.is_file():
        raise ValueError('Missing source-size checker: scripts/check_source_sizes.py')
    return subprocess.run([sys.executable, '-I', str(checker)]).returncode == 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('test', 'syntax', 'check', 'tokens', 'sizes'))
    args = parser.parse_args()
    actual = platform.python_version()
    print(f'Python: {actual}; CI reference: {CI_PYTHON}; platform: {sys.platform}', flush=True)
    if sys.version_info < (3, 11):
        parser.exit(1, 'Python 3.11 or newer is required.\n')
    if actual != CI_PYTHON:
        print('Version differs from CI; this result covers only the current interpreter.', flush=True)
    try:
        os.chdir(ROOT)
        if args.command == 'tokens':
            return 0 if check_tokens() else 1
        if args.command == 'sizes':
            return 0 if check_source_sizes() else 1
        python_files('src')
        python_files('tests')
        if args.command in ('test', 'check') and not run_tests():
            return 1
        if args.command in ('syntax', 'check'):
            return 0 if check_syntax() else 1
        return 0
    except (OSError, ValueError, SyntaxError, ImportError) as error:
        print(f'Check failed: {error}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
