"""Own the concrete native IO worker and validate native terminal receipts."""
import json
import os
from pathlib import Path
import platform
import re
import shutil
import sys
import tempfile

from check_mac_enrollment_lookup import command, completed
from check_mac_native_pump import CRATE, validate_dependencies
from check_mac_rust_owner import build_swift

CASES = {'supervisor-unproven-exit-two': 17,
         'supervisor-finite-coalesced': 17,
         'supervisor-discard-pending': 23,
         'supervisor-discard-trailing': 23,
         'supervisor-finite-malformed': 17,
         'supervisor-finite-unwind': 17,
         'supervisor-finite-unknown': 17,
         'supervisor-prepared': 16,
         'supervisor-confirmed': 23,
         'supervisor-unknown': 21,
         'supervisor-missing': 21,
         'supervisor-malformed': 21,
         'supervisor-invalid': 17,
         'supervisor-rejected': 17,
         'supervisor-signal': 17,
         'supervisor-lost-receipt': 17,
         'supervisor-unwind': 17,
         'supervisor-drop': 16,
         'supervisor-startup-sealed': 16}
NO_CHILD = {'supervisor-normal', 'supervisor-prepared', 'supervisor-startup-sealed'}
TERMINAL = {'supervisor-rejected', 'supervisor-confirmed', 'supervisor-drop'}


def validate_report(mode, output):
    expected = 10 if mode == 'supervisor-normal' else CASES.get(mode)
    child = int(mode not in NO_CHILD)
    unknown = int(mode not in NO_CHILD and mode not in TERMINAL)
    match = re.fullmatch(r'NATIVE_SUPERVISOR_FIXTURE mode=([a-z-]+) passed=(\d+) failed=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) open=(\d+) unknown=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, child, child, 0, unknown, 0):
        raise ValueError('incomplete or uncollected native supervisor fixture')
    return expected


def execute_mode(executable, mode, progress):
    if mode not in {'supervisor-normal', *CASES}:
        raise ValueError('unsupported native supervisor mode')
    progress.update(phase='fixture', mode=mode, returncode=None, condition=None, child_exit=None)
    result = completed([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
    progress['returncode'] = result.returncode
    if result.returncode:
        match = re.search(r'\nnative supervisor condition ([1-9][0-9]?)\n', result.stderr)
        limit = 10 if mode == 'supervisor-normal' else CASES[mode]
        if match and 1 <= int(match.group(1)) <= limit:
            progress['condition'] = int(match.group(1))
        child = re.search(r'\nNATIVE_SUPERVISOR_CHILD_EXIT ([0-9]{1,3})\n', '\n' + result.stderr)
        if child and int(child.group(1)) <= 255:
            progress['child_exit'] = int(child.group(1))
        raise ValueError('native supervisor fixture failed')
    progress['phase'] = 'report'
    return validate_report(mode, result.stdout)


def build_command(arguments, env, progress):
    result = completed(arguments, env)
    progress['returncode'] = result.returncode
    if result.returncode:
        raise ValueError('native supervisor build failed')
    return result.stdout


def run(progress=None):
    if progress is None:
        progress = {}
    progress.update(phase='invocation', mode=None, returncode=None, condition=None, child_exit=None)
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported native supervisor invocation')
    arch = {'arm64': 'arm64', 'x86_64': 'x86_64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported native Pump architecture')
    progress['phase'] = 'dependencies'
    validate_dependencies()
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR', 'CARGO_HOME')}
    progress['phase'] = 'toolchain'
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
            label = 'fixture' if fixture else 'normal'
            progress.update(phase=label + '-swift', mode=None, returncode=None, condition=None, child_exit=None)
            build_swift(swift, sdk, arch, fixture, directory, root / 'swift-cache', env)
            variant = dict(env, CARGO_TARGET_DIR=str(directory / 'target'))
            flags = ['-Dwarnings', '-L', 'native=' + str(directory), '-l', 'dylib=ResidentPreparation',
                     '-C', 'link-arg=-Wl,-rpath,' + str(directory)]
            flags += ['--cfg', 'resident_native_source']
            if fixture:
                flags += ['--cfg', 'resident_launch_fixture']
            variant['CARGO_ENCODED_RUSTFLAGS'] = '\x1f'.join(flags)
            manifest = ['--manifest-path', str(CRATE / 'Cargo.toml')]
            progress['phase'] = label + '-build'
            build_command([cargo, 'build', '--locked', '--offline', '--jobs', '2'] + manifest, variant, progress)
            progress.update(phase=label + '-clippy', returncode=None)
            build_command([cargo, 'clippy', '--locked', '--offline', '--jobs', '2'] + manifest + ['--', '-Dwarnings'], variant, progress)
            executable = directory / 'target/debug/native-pump-source-fixture'
            for mode in CASES if fixture else ('supervisor-normal',):
                passed += execute_mode(executable, mode, progress)
    print(json.dumps({'fixture': 'native-io-supervisor-source', 'passed': passed,
                      'normalModes': 1, 'fixtureModes': 19, 'activeModes': 17,
                      'fixtureChildrenStarted': 17, 'fixtureChildrenWaited': 17,
                      'physicalActiveEndpointsClosed': 68, 'unlaunchedEndpointsClosed': 4,
                      'actualActiveIoWorkerJoins': 15, 'directDiscardWorkerJoins': 2,
                      'fixtureControlGatesClosed': 1, 'preparationWorkerJoins': 21,
                      'retainedUnknownRecords': 14, 'retainedTerminalRecords': 3, 'lockedOffline': True,
                      'existingDependencyPackages': 11, 'clippyVariants': 2,
                      'producerLaunches': 0, 'hostOnly': True,
                      'scope': 'same affine native IO worker / Wait / component receipts; no outer joins / Quit permit'}))


if __name__ == '__main__':
    progress = {}
    try:
        run(progress)
    except (OSError, ValueError, KeyError):
        print('NATIVE_SUPERVISOR_FAILED ' + json.dumps(progress, sort_keys=True), file=sys.stderr)
        raise SystemExit(1)
