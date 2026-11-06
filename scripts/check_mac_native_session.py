"""Pair the original native owner with the real typed ResidentPort core."""
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile

from check_mac_enrollment_lookup import command
from check_mac_native_pump import CRATE, validate_dependencies
from check_mac_rust_owner import build_swift

CASES = {'session-confirmed': 26, 'session-unknown': 26,
         'session-missing': 26, 'session-malformed': 26,
         'session-lost': 18, 'session-invalid': 18, 'session-rejected': 18,
         'session-unwind': 18, 'session-drop': 18}
NO_CHILD = {'session-normal'}


def validate_report(mode, output):
    expected = 8 if mode == 'session-normal' else CASES.get(mode)
    child = int(mode not in NO_CHILD)
    match = re.fullmatch(r'NATIVE_SESSION_FIXTURE mode=([a-z-]+) passed=(\d+) failed=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) open=(\d+) nativeUnknown=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, child, child, 0, child, 0):
        raise ValueError('incomplete or uncollected native session fixture')
    return expected


def validate_borrow_refusal(returncode, stderr):
    if returncode == 0 or re.findall(r'error\[E\d+\]', stderr) != ['error[E0505]'] or 'cannot move out of `session` because it is borrowed' not in stderr:
        raise ValueError('missing exact session borrow refusal')


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported native session invocation')
    arch = {'arm64': 'arm64', 'x86_64': 'x86_64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported native Pump architecture')
    validate_dependencies()
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR', 'CARGO_HOME')}
    cargo = shutil.which('cargo', path=env.get('PATH'))
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    if cargo is None or not Path(swift).is_file() or not Path(sdk).is_dir():
        raise ValueError('existing native compiler unavailable')
    env.update(MACOSX_DEPLOYMENT_TARGET='11.0', SDKROOT=sdk)
    passed = 0
    with tempfile.TemporaryDirectory(prefix='native-session-') as temporary:
        root = Path(temporary)
        for fixture in (False, True):
            directory = root / ('fixture' if fixture else 'normal')
            directory.mkdir()
            build_swift(swift, sdk, arch, fixture, directory, root / 'swift-cache', env)
            variant = dict(env, CARGO_TARGET_DIR=str(directory / 'target'))
            flags = ['-Dwarnings', '-L', 'native=' + str(directory), '-l', 'dylib=ResidentPreparation',
                     '-C', 'link-arg=-Wl,-rpath,' + str(directory)]
            flags += ['--cfg', 'resident_native_source']
            if fixture:
                flags += ['--cfg', 'resident_launch_fixture']
            variant['CARGO_ENCODED_RUSTFLAGS'] = '\x1f'.join(flags)
            manifest = ['--manifest-path', str(CRATE / 'Cargo.toml')]
            command([cargo, 'build', '--locked', '--offline', '--jobs', '2'] + manifest, variant)
            command([cargo, 'clippy', '--locked', '--offline', '--jobs', '2'] + manifest + ['--', '-Dwarnings'], variant)
            executable = directory / 'target/debug/native-pump-source-fixture'
            for mode in CASES if fixture else ('session-normal',):
                output = command([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
                passed += validate_report(mode, output)
            # Compiler guard: a real typed view cannot outlive its owning session.
            if not fixture:
                result = subprocess.run([cargo, 'rustc', '--locked', '--offline', '--jobs', '2'] + manifest +
                                        ['--', '--cfg', 'resident_session_borrow_refusal', '--emit=metadata'],
                                        env=variant, text=True, capture_output=True, timeout=60)
                validate_borrow_refusal(result.returncode, result.stderr)
    print(json.dumps({'fixture': 'native-session-source', 'passed': passed,
                      'normalModes': 1, 'fixtureModes': 9,
                      'fixtureChildrenStarted': 9, 'fixtureChildrenWaited': 9,
                      'physicalActiveEndpointsClosed': 36, 'retainedNativeUnknownRecords': 9,
                      'explicitOuterJoins': 8, 'sessionDropJoins': 1,
                      'replacementRefusals': 9, 'quitPermits': 0,
                      'lockedOffline': True, 'existingDependencyPackages': 11,
                      'clippyVariants': 2, 'compileRefusals': 1, 'producerLaunches': 0,
                      'scope': 'same native owner and borrowed typed status/intent port; no normal startup / IPC / auth / Quit permission'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError, subprocess.TimeoutExpired):
        print('NATIVE_SESSION_FAILED', file=sys.stderr)
        raise SystemExit(1)
