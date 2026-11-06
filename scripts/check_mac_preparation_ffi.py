"""Compile C/Swift preparation ABI; run own inert pipe/thread fixtures only."""
import json
import os
from pathlib import Path
import platform
import re
import sys
import tempfile

from check_mac_enrollment_lookup import command
from check_mac_launch_requirement import NATIVE, validate_weak_link

CASES = {'lifetime': 20, 'io': 10, 'unknown': 12, 'thread-exit': 12, 'partial': 10, 'overflow': 10, 'peer-repeat': 16}


def validate_report(mode, output):
    expected = CASES.get(mode)
    match = re.fullmatch(r'NATIVE_PREPARATION_FFI mode=([a-z-]+) passed=(\d+) failed=(\d+) launched=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, 0):
        raise ValueError('incomplete, failed or launched fixture')
    return expected


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported native fixture invocation')
    arch = {'arm64': 'arm64', 'x86_64': 'x86_64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported native fixture architecture')
    env = {key: value for key, value in os.environ.items() if key in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR')}
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    clang = command(['/usr/bin/xcrun', '--find', 'clang'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    if not Path(swift).is_file() or not Path(clang).is_file() or not Path(sdk).is_dir():
        raise ValueError('native tools unavailable')
    with tempfile.TemporaryDirectory(prefix='resident-preparation-ffi-') as temporary:
        directory = Path(temporary)
        sources = [str(NATIVE / name) for name in ('ResidentLaunchPreparation.swift', 'OwnedPreparationIO.swift', 'ResidentPreparationABI.swift', 'PreparedByteIO.swift')]
        header = str(NATIVE / 'ResidentPreparation.h')
        passed = 0
        for target_arch in (arch, 'x86_64' if arch == 'arm64' else 'arm64'):
            target = target_arch + '-apple-macosx11.0'  # Fixture, not app minimum OS.
            base = [swift, '-swift-version', '6', '-warnings-as-errors', '-sdk', sdk,
                    '-target', target, '-parse-as-library', '-import-objc-header', header,
                    '-module-cache-path', str(directory / ('cache-' + target_arch))]
            command(base + ['-whole-module-optimization', '-emit-object'] + sources + ['-o', str(directory / (target_arch + '-normal.o'))], env)
            driver = directory / (target_arch + '-driver.o')
            command([clang, '-isysroot', sdk, '-target', target, '-std=c11', '-Wall', '-Wextra', '-Werror',
                     '-DRESIDENT_LAUNCH_FIXTURE', '-c', str(NATIVE / 'fixtures/FFIDriver.c'), '-o', str(driver)], env)
            executable = directory / (target_arch + '-fixture')
            command(base + ['-D', 'RESIDENT_LAUNCH_FIXTURE', '-Xcc', '-DRESIDENT_LAUNCH_FIXTURE'] + sources + [str(driver),
                            '-Xlinker', '-weak_framework', '-Xlinker', 'LightweightCodeRequirements', '-o', str(executable)], env)
            validate_weak_link(command(['/usr/bin/otool', '-l', str(executable)], env))
            if target_arch == arch:
                for mode in CASES:
                    report = command([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
                    passed += validate_report(mode, report)
    print(json.dumps({'fixture': 'resident-preparation-ffi', 'passed': passed, 'modes': len(CASES),
                      'normalCompileTargets': 2, 'fixtureCompileTargets': 2, 'foreignExecution': False,
                      'producerLaunches': 0, 'scope': 'unlaunched owner; anonymous pipe EOF and fixture pthread joins only'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_PREPARATION_FFI_FIXTURE_FAILED', file=sys.stderr)
        raise SystemExit(1)
