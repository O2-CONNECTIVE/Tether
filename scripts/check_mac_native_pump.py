"""Use real native IO with the existing Pump; no launch or UI binding."""
import json
import os
from pathlib import Path
import platform
import re
import shutil
import sys
import tempfile
import tomllib

from check_mac_enrollment_lookup import command
from check_mac_rust_owner import NATIVE, build_swift

CASES = {'rejection': 13, 'partial-rejection': 15, 'intent': 17, 'invalid': 8,
         'eof': 9, 'half-close': 13, 'half-close-unknown': 9, 'discard': 11}
CRATE = NATIVE / 'fixtures/pump_source'
ROOT = NATIVE.parents[3]


def validate_report(mode, output):
    expected = 1 if mode == 'normal' else CASES.get(mode)
    match = re.fullmatch(r'NATIVE_PUMP_FIXTURE mode=([a-z-]+) passed=(\d+) failed=(\d+) launched=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, 0):
        raise ValueError('incomplete or failed native Pump fixture')
    return expected


def validate_dependencies():
    baseline = tomllib.loads((ROOT / 'ui/tauri/src-tauri/Cargo.lock').read_text())
    fixture = tomllib.loads((CRATE / 'Cargo.lock').read_text())
    known = {(p['name'], p['version'], p.get('checksum')) for p in baseline['package']}
    external = [p for p in fixture['package'] if p.get('source')]
    if len(external) != 11 or any((p['name'], p['version'], p.get('checksum')) not in known for p in external):
        raise ValueError('fixture dependency outside the existing app lock')


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported native Pump fixture invocation')
    arch = {'arm64': 'arm64', 'x86_64': 'x86_64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported native Pump architecture')
    validate_dependencies()
    env = {key: value for key, value in os.environ.items() if key in ('PATH', 'HOME', 'TMPDIR', 'CARGO_HOME')}
    cargo = shutil.which('cargo', path=env.get('PATH'))
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    if cargo is None or not Path(swift).is_file() or not Path(sdk).is_dir():
        raise ValueError('existing native compiler unavailable')
    env.update(MACOSX_DEPLOYMENT_TARGET='11.0', SDKROOT=sdk)
    with tempfile.TemporaryDirectory(prefix='native-pump-source-') as temporary:
        root = Path(temporary)
        passed = 0
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
            command([cargo, 'build', '--locked', '--offline', '--jobs', '2', '--manifest-path', str(CRATE / 'Cargo.toml')], variant)
            command([cargo, 'clippy', '--locked', '--offline', '--jobs', '2', '--manifest-path', str(CRATE / 'Cargo.toml'), '--', '-Dwarnings'], variant)
            executable = directory / 'target/debug/native-pump-source-fixture'
            for mode in CASES if fixture else ('normal',):
                passed += validate_report(mode, command([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'}))
    print(json.dumps({'fixture': 'native-pump-source', 'passed': passed, 'normalModes': 1, 'nativeModes': len(CASES),
                      'lockedOffline': True, 'existingDependencyPackages': 11, 'hostOnly': True,
                      'clippyVariants': 2,
                      'producerLaunches': 0, 'scope': 'prepared native IO and real Pump protocol core; no auth / Wait / joins proof'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_PUMP_SOURCE_FIXTURE_FAILED', file=sys.stderr)
        raise SystemExit(1)
