"""Compile native preparation and run only its inert synthetic fixture.

No GUI/producer launch, signing, enrollment or real identity inspection.
"""
import json
import os
from pathlib import Path
import platform
import re
import sys
import tempfile

from check_mac_enrollment_lookup import command

ROOT = Path(__file__).resolve().parents[1]
NATIVE = ROOT / 'ui/tauri/native/resident-launch'


def validate_report(output):
    lines = [line for line in output.splitlines() if line.strip()]
    if len(lines) != 1:
        raise ValueError('fixture did not have one terminal result')
    match = re.fullmatch(r'NATIVE_REQUIREMENT_FIXTURE passed=(\d+) failed=(\d+) launched=(\d+)', lines[0])
    if not match or tuple(map(int, match.groups())) != (35, 0, 0):
        raise ValueError('fixture was incomplete, failed or launched a process')
    return 35


def validate_weak_link(output):
    commands = re.split(r'(?m)^Load command ', output)
    matching = [item for item in commands if re.search(r'(?m)^\s+name /System/Library/Frameworks/LightweightCodeRequirements\.framework/', item)]
    if len(matching) != 1 or not re.search(r'(?m)^\s+cmd LC_LOAD_WEAK_DYLIB$', matching[0]):
        raise ValueError('new framework was not weak-linked')


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported native fixture invocation')
    arch = {'arm64': 'arm64', 'x86_64': 'x86_64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported native fixture architecture')
    env = {key: value for key, value in os.environ.items() if key in ('PATH', 'HOME', 'TMPDIR')}
    # Existing developer tools only. No xcode-select/install/default changes.
    compiler = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    if not Path(compiler).is_file() or not Path(sdk).is_dir():
        raise ValueError('native compiler or SDK unavailable')
    with tempfile.TemporaryDirectory(prefix='resident-launch-requirement-') as temporary:
        directory = Path(temporary)
        source = NATIVE / 'ResidentLaunchPreparation.swift'
        fixture = NATIVE / 'fixtures/Driver.swift'
        # This is an explicit fixture compile target, NOT the app minimum OS.
        for target_arch in (arch, 'x86_64' if arch == 'arm64' else 'arm64'):
            base = [compiler, '-swift-version', '6', '-warnings-as-errors', '-sdk', sdk,
                    '-target', target_arch + '-apple-macosx11.0', '-parse-as-library',
                    '-module-cache-path', str(directory / ('cache-' + target_arch))]
            sources = [str(source), str(NATIVE / 'OwnedPreparationIO.swift'), str(NATIVE / 'PreparedByteIO.swift')]
            command(base + ['-whole-module-optimization', '-emit-object'] + sources + ['-o', str(directory / (target_arch + '.o'))], env)
            executable = directory / (target_arch + '-fixture')
            command(base + ['-D', 'RESIDENT_LAUNCH_FIXTURE'] + sources + [str(fixture),
                            '-Xlinker', '-weak_framework', '-Xlinker', 'LightweightCodeRequirements',
                            '-o', str(executable)], env)
            validate_weak_link(command(['/usr/bin/otool', '-l', str(executable)], env))
            if target_arch == arch:
                report = command([str(executable)], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
                passed = validate_report(report)
    print(json.dumps({'fixture': 'resident-launch-requirement', 'passed': passed,
                      'normalCompileTargets': 2, 'fixtureCompileTargets': 2,
                      'foreignExecution': False, 'producerLaunches': 0,
                      'scope': 'typed API and inert owner only; no launch enforcement proof'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_LAUNCH_REQUIREMENT_FIXTURE_FAILED', file=sys.stderr)
        raise SystemExit(1)
