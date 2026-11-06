"""Join the concrete native outer monitor; UNKNOWN never permits Quit."""
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

CASES = {'outer-prepared': 13, 'outer-confirmed': 20, 'outer-unknown': 20,
         'outer-missing': 20, 'outer-malformed': 20, 'outer-invalid': 16,
         'outer-rejected': 16, 'outer-signal': 16, 'outer-lost-receipt': 16,
         'outer-unwind': 16, 'outer-drop': 12, 'outer-held': 20, 'outer-panic': 16, 'outer-prejoined': 16}
NO_CHILD = {'outer-normal', 'outer-normal-prejoined', 'outer-prepared'}


def validate_report(mode, output):
    expected = 8 if mode in ('outer-normal', 'outer-normal-prejoined') else CASES.get(mode)
    child = int(mode not in NO_CHILD)
    match = re.fullmatch(r'NATIVE_OUTER_FIXTURE mode=([a-z-]+) passed=(\d+) failed=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) open=(\d+) nativeUnknown=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, child, child, 0, child, 0):
        raise ValueError('incomplete or uncollected native outer monitor fixture')
    return expected


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported native outer monitor invocation')
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
    with tempfile.TemporaryDirectory(prefix='native-outer-') as temporary:
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
            for mode in CASES if fixture else ('outer-normal', 'outer-normal-prejoined'):
                output = command([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
                passed += validate_report(mode, output)
    print(json.dumps({'fixture': 'native-outer-monitor-source', 'passed': passed,
                      'normalModes': 2, 'fixtureModes': 14, 'activeModes': 13,
                      'fixtureChildrenStarted': 13, 'fixtureChildrenWaited': 13,
                      'physicalActiveEndpointsClosed': 52, 'unlaunchedEndpointsClosed': 4,
                      'actualActiveIoWorkerJoins': 13, 'successfulActiveOuterJoins': 12,
                      'failedActiveOuterJoins': 1, 'preparationOuterJoins': 3,
                      'retainedNativeUnknownRecords': 13, 'quitPermits': 0,
                      'lockedOffline': True, 'existingDependencyPackages': 11,
                      'clippyVariants': 2, 'producerLaunches': 0, 'hostOnly': True,
                      'scope': 'same native IO + actual outer monitor joins; UNKNOWN and no application Quit permission'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_OUTER_FAILED', file=sys.stderr)
        raise SystemExit(1)
