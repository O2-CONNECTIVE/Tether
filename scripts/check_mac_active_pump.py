"""Drive the existing Pump with the same active native owner; source fixture only."""
import json
import os
from pathlib import Path
import platform
import re
import shutil
import sys
import tempfile

from check_mac_enrollment_lookup import command
from check_mac_native_pump import CRATE, validate_dependencies, validate_report as validate_normal
from check_mac_rust_owner import build_swift

CASES = {'active-confirmed': 16, 'active-unknown': 16, 'active-missing': 16,
         'active-malformed': 16, 'active-invalid': 13, 'active-rejected': 13,
         'active-signal': 13, 'active-lost-receipt': 13, 'active-panic': 11}


def validate_report(mode, output):
    expected = CASES.get(mode)
    match = re.fullmatch(r'NATIVE_ACTIVE_PUMP_FIXTURE mode=([a-z-]+) passed=(\d+) failed=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) open=(\d+) unknown=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, 1, 1, 0, 1, 0):
        raise ValueError('incomplete or uncollected active Pump fixture')
    return expected


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported active Pump invocation')
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
    with tempfile.TemporaryDirectory(prefix='active-native-pump-') as temporary:
        root = Path(temporary)
        for fixture in (False, True):
            directory = root / ('fixture' if fixture else 'normal')
            directory.mkdir()
            build_swift(swift, sdk, arch, fixture, directory, root / 'swift-cache', env)
            variant = dict(env, CARGO_TARGET_DIR=str(directory / 'target'))
            flags = ['-Dwarnings', '-L', 'native=' + str(directory), '-l', 'dylib=ResidentPreparation',
                     '-C', 'link-arg=-Wl,-rpath,' + str(directory)]
            if fixture:
                flags += ['--cfg', 'resident_native_source', '--cfg', 'resident_launch_fixture']
            variant['CARGO_ENCODED_RUSTFLAGS'] = '\x1f'.join(flags)
            manifest = ['--manifest-path', str(CRATE / 'Cargo.toml')]
            command([cargo, 'build', '--locked', '--offline', '--jobs', '2'] + manifest, variant)
            command([cargo, 'clippy', '--locked', '--offline', '--jobs', '2'] + manifest + ['--', '-Dwarnings'], variant)
            executable = directory / 'target/debug/native-pump-source-fixture'
            for mode in CASES if fixture else ('normal',):
                output = command([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
                passed += validate_report(mode, output) if fixture else validate_normal(mode, output)
    print(json.dumps({'fixture': 'active-native-pump-source', 'passed': passed, 'normalModes': 1, 'activeModes': 9,
                      'fixtureChildrenStarted': 9, 'fixtureChildrenWaited': 9, 'physicalOriginalEndpointsClosed': 36,
                      'actualIoWorkerJoins': 9, 'retainedUnknownRecords': 9,
                      'lockedOffline': True, 'existingDependencyPackages': 11, 'clippyVariants': 2,
                      'producerLaunches': 0, 'hostOnly': True,
                      'scope': 'same native owner and shared IO loop; no native Supervisor / full joins / Quit proof'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_ACTIVE_PUMP_FAILED', file=sys.stderr)
        raise SystemExit(1)
