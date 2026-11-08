"""Own the concrete native IO worker; joined components never grant Quit."""
import json
import os
from pathlib import Path
import platform
import re
import shutil
import sys
import tempfile

from check_mac_enrollment_lookup import command
from check_mac_native_pump import CRATE, validate_dependencies
from check_mac_rust_owner import build_swift

CASES = {'supervisor-discard-pending': 23, 'supervisor-discard-trailing': 23, 'supervisor-finite-malformed': 14, 'supervisor-finite-unwind': 14,
         'supervisor-finite-unknown': 14, 'supervisor-prepared': 13, 'supervisor-confirmed': 20,
         'supervisor-unknown': 18, 'supervisor-missing': 18, 'supervisor-malformed': 18,
         'supervisor-invalid': 14, 'supervisor-rejected': 14, 'supervisor-signal': 14,
         'supervisor-lost-receipt': 14, 'supervisor-unwind': 14, 'supervisor-drop': 13}
NO_CHILD = {'supervisor-normal', 'supervisor-prepared'}


def validate_report(mode, output):
    expected = 7 if mode == 'supervisor-normal' else CASES.get(mode)
    child = int(mode not in NO_CHILD)
    match = re.fullmatch(r'NATIVE_SUPERVISOR_FIXTURE mode=([a-z-]+) passed=(\d+) failed=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) open=(\d+) unknown=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, child, child, 0, child, 0):
        raise ValueError('incomplete or uncollected native supervisor fixture')
    return expected


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported native supervisor invocation')
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
    with tempfile.TemporaryDirectory(prefix='native-supervisor-') as temporary:
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
            for mode in CASES if fixture else ('supervisor-normal',):
                output = command([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
                passed += validate_report(mode, output)
    print(json.dumps({'fixture': 'native-io-supervisor-source', 'passed': passed,
                      'normalModes': 1, 'fixtureModes': 16, 'activeModes': 15,
                      'fixtureChildrenStarted': 15, 'fixtureChildrenWaited': 15,
                      'physicalActiveEndpointsClosed': 60, 'unlaunchedEndpointsClosed': 4,
                      'actualActiveIoWorkerJoins': 13, 'directDiscardWorkerJoins': 2,
                      'fixtureControlGatesClosed': 1, 'preparationWorkerJoins': 2,
                      'retainedUnknownRecords': 15, 'lockedOffline': True,
                      'existingDependencyPackages': 11, 'clippyVariants': 2,
                      'producerLaunches': 0, 'hostOnly': True,
                      'scope': 'same affine native IO worker / Wait / component receipts; no outer joins / Quit permit'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_SUPERVISOR_FAILED', file=sys.stderr)
        raise SystemExit(1)
