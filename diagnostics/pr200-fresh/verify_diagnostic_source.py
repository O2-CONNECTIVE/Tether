"""Fail closed unless native helpers exactly project the reviewed diagnostic source."""
import argparse
import os
from pathlib import Path
import re
import subprocess
import sys

SOURCE = '7e89dda809b54663f2c394126a51ecf44d121882'
TREE = 'b0665e9e8a0c16c176e5e74d9755a2d0e2f52cfc'
HELPERS = ('phase2.py', 'report.py', 'profile_contract.py', 'evidence_schema.py',
           'powershell_preflight.py', 'capability-shape.mjs', 'capture-capability.mjs',
           'path-validation.mjs', 'observe-profile.ps1', 'observer-detail.ps1',
           'observer-reasons.test.ps1', 'observer-detail.test.ps1',
           'parse-predicate-sources.ps1', 'audit.ps1')


def need(value):
    if not value:
        raise ValueError('DIAGNOSTIC_SOURCE_REJECTED')


def git(root, *args):
    return subprocess.run(['git', '-C', str(root), *args], check=True,
                          capture_output=True, timeout=10).stdout


def verify(control, diagnostic, expected):
    need(isinstance(expected, str) and re.fullmatch('[0-9a-f]{40}', expected))
    need(git(control, 'rev-parse', 'HEAD').decode().strip() == expected)
    need(git(diagnostic, 'rev-parse', 'HEAD').decode().strip() == SOURCE)
    need(git(diagnostic, 'rev-parse', 'HEAD^{tree}').decode().strip() == TREE)
    for root in (control, diagnostic):
        need(not git(root, 'status', '--porcelain', '--untracked-files=all').strip())
    for name in HELPERS:
        path = 'diagnostics/pr200-fresh/' + name
        original = git(diagnostic, 'show', SOURCE + ':' + path)
        need(git(control, 'show', expected + ':' + path) == original)
        for root in (control, diagnostic):
            need((root / path).read_bytes() in (original, original.replace(b'\n', b'\r\n')))
    return True


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--control', type=Path, required=True)
    parser.add_argument('--diagnostic', type=Path, required=True)
    args = parser.parse_args()
    try:
        verify(args.control, args.diagnostic, os.environ.get('EXPECTED_CONTROL'))
    except Exception:
        print('DIAGNOSTIC_SOURCE_REJECTED')
        return 1
    print('FIXED_DIAGNOSTIC_SOURCE_VERIFIED helpers=14')
    return 0


if __name__ == '__main__':
    sys.exit(main())
