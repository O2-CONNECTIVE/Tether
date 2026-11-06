"""Compile a same-Process active IO owner and run only fixed self-child fixtures."""
import json
import os
from pathlib import Path
import platform
import re
import sys
import tempfile

from check_mac_enrollment_lookup import command
from check_mac_launch_requirement import NATIVE, validate_weak_link

CASES = {'echo': (32, 1, 0), 'early': (32, 1, 1), 'lost': (32, 1, 1),
         'missing': (27, 1, 1), 'guards': (6, 0, 0)}


def validate_report(mode, output):
    expected = CASES.get(mode)
    match = re.fullmatch(r'NATIVE_ACTIVE_IO mode=([a-z-]+) passed=(\d+) failed=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) open=(\d+) unknown=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode:
        raise ValueError('missing active IO fixture')
    passed, children, unknown = expected
    if tuple(map(int, match.groups()[1:])) != (passed, 0, children, children, 0, unknown, 0):
        raise ValueError('incomplete, failed or uncollected active IO fixture')
    return passed


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported active IO fixture invocation')
    arch = {'arm64': 'arm64', 'x86_64': 'x86_64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported active IO fixture architecture')
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR')}
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    if not Path(swift).is_file() or not Path(sdk).is_dir():
        raise ValueError('native compiler unavailable')
    with tempfile.TemporaryDirectory(prefix='resident-active-io-') as temporary:
        directory = Path(temporary)
        passed = 0
        sources = [str(NATIVE / n) for n in ('ResidentLaunchPreparation.swift', 'OwnedPreparationIO.swift', 'PreparedByteIO.swift')]
        for target in (arch, 'x86_64' if arch == 'arm64' else 'arm64'):
            base = [swift, '-swift-version', '6', '-warnings-as-errors', '-sdk', sdk,
                    '-target', target + '-apple-macosx11.0', '-parse-as-library',
                    '-module-cache-path', str(directory / ('cache-' + target))]
            command(base + ['-whole-module-optimization', '-emit-object'] + sources +
                    ['-o', str(directory / (target + '-normal.o'))], env)
            executable = directory / (target + '-active-io-fixture')
            command(base + ['-D', 'RESIDENT_LAUNCH_FIXTURE'] + sources +
                    [str(NATIVE / 'fixtures/ActiveIODriver.swift'), '-Xlinker', '-weak_framework',
                     '-Xlinker', 'LightweightCodeRequirements', '-o', str(executable)], env)
            validate_weak_link(command(['/usr/bin/otool', '-l', str(executable)], env))
            if target == arch:
                for mode in CASES:
                    passed += validate_report(mode, command([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'}))
    print(json.dumps({'fixture': 'same-foundation-process-active-io', 'passed': passed,
                      'fixtureChildrenStarted': 4, 'fixtureChildrenWaited': 4, 'producerLaunches': 0,
                      'normalCompileTargets': 2, 'fixtureCompileTargets': 2, 'foreignExecution': False,
                      'scope': 'self-child endpoint transfer, IO EOF and exact Wait only; no producer / C ABI active binding / active joins / cleanup permission'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_ACTIVE_IO_FIXTURE_FAILED', file=sys.stderr)
        raise SystemExit(1)
