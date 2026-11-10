"""Link real Rust/Swift preparation, never a mock FFI or launched producer."""
import json
import os
from pathlib import Path
import platform
import re
import shutil
import sys
import tempfile

from check_mac_enrollment_lookup import command, completed
from check_mac_launch_requirement import NATIVE, validate_weak_link

CASES = {'launch-refusal': 10, 'lifetime': 12, 'drop': 6, 'io': 7, 'unknown': 9,
         'acquire-unknown': 7, 'thread-exit': 5, 'unwind': 6,
         'byte-stream': 23, 'byte-unknown': 9, 'byte-reject': 7}
NORMAL_EXPORTS = {'_rh_resident_preparation_' + name for name in ('create', 'startup', 'launch', 'state', 'retire', 'read', 'write', 'half_close', 'exit', 'retire_active_io', 'abandon_active', 'request_active_close', 'discard_observation', 'finish_active')}
FIXTURE_EXPORTS = {'_rh_fixture_preparation_' + name for name in
                   ('create', 'probe', 'exchange', 'close_count', 'open_count', 'exhaust', 'retire_calls', 'peer', 'send', 'receive', 'create_active', 'collect_active', 'active_stats', 'retained_state', 'release_control_gate')}


def validate_report(mode, output):
    expected = 1 if mode == 'normal' else CASES.get(mode)
    match = re.fullmatch(r'NATIVE_RUST_OWNER mode=([a-z-]+) passed=(\d+) failed=(\d+) launched=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, 0):
        raise ValueError('incomplete or failed Rust owner fixture')
    return expected


def validate_exports(output, fixture):
    exports = {line.strip() for line in output.splitlines() if line.startswith('_rh_')}
    if exports != NORMAL_EXPORTS | (FIXTURE_EXPORTS if fixture else set()):
        raise ValueError('missing or unexpected native exports')


def validate_rejection(result, code, message):
    if result.returncode == 0:
        raise ValueError('unsafe Rust fixture unexpectedly compiled')
    diagnostics = [json.loads(line) for line in result.stderr.splitlines() if line.strip()]
    errors = [entry for entry in diagnostics if entry.get('level') == 'error' and entry.get('code')]
    if len(errors) != 1 or errors[0]['code'].get('code') != code or message not in errors[0].get('message', ''):
        raise ValueError('Rust failed for the wrong reason')


def rust_variant(rust, directory, fixture, env):
    flags = ['--cfg', 'resident_launch_fixture'] if fixture else []
    base = [rust, '--edition=2024', '-Dwarnings'] + flags
    library = directory / 'libresident_preparation.rlib'
    command(base + ['--crate-type=rlib', '--crate-name', 'resident_preparation',
                    str(NATIVE / 'rust/owner.rs'), '-o', str(library)], env)
    external = ['--extern', 'resident_preparation=' + str(library)]
    executable = directory / 'rust-owner-fixture'
    command(base + [str(NATIVE / 'fixtures/RustDriver.rs')] + external +
            ['-L', 'native=' + str(directory), '-l', 'dylib=ResidentPreparation',
             '-C', 'link-arg=-Wl,-rpath,@executable_path', '-o', str(executable)], env)
    passed = 0
    for mode in CASES if fixture else ('normal',):
        passed += validate_report(mode, command([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'}))
    for name, code, message in [('NotSend', 'E0277', 'cannot be sent'), ('NotSync', 'E0277', 'cannot be shared'),
                                ('BorrowRetire', 'E0499', 'more than once'),
                                ('BorrowDrop', 'E0505', 'because it is borrowed')]:
        result = completed(base + [str(NATIVE / ('fixtures/' + name + '.rs'))] + external +
                           ['--emit=metadata', '--error-format=json', '-o', str(directory / (name + '.rmeta'))], env)
        validate_rejection(result, code, message)
    if not fixture:
        result = completed(base + [str(NATIVE / 'fixtures/NoNormalFixture.rs')] + external +
                           ['--emit=metadata', '--error-format=json', '-o', str(directory / 'NoNormalFixture.rmeta')], env)
        validate_rejection(result, 'E0599', 'fixture')
    return passed


def build_swift(swift, sdk, target_arch, fixture, directory, cache, env):
    sources = [str(NATIVE / name) for name in ('ResidentLaunchPreparation.swift', 'OwnedPreparationIO.swift', 'ResidentPreparationABI.swift', 'PreparedByteIO.swift')]
    library = directory / 'libResidentPreparation.dylib'
    base = [swift, '-swift-version', '6', '-warnings-as-errors', '-sdk', sdk,
            '-target', target_arch + '-apple-macosx11.0', '-parse-as-library',
            '-module-cache-path', str(cache), '-import-objc-header', str(NATIVE / 'ResidentPreparation.h')]
    if fixture:
        base += ['-D', 'RESIDENT_LAUNCH_FIXTURE', '-Xcc', '-DRESIDENT_LAUNCH_FIXTURE']
    command(base + ['-emit-library', '-module-name', 'ResidentPreparation'] + sources +
            ['-Xlinker', '-weak_framework', '-Xlinker', 'LightweightCodeRequirements',
             '-Xlinker', '-install_name', '-Xlinker', '@rpath/libResidentPreparation.dylib', '-o', str(library)], env)
    validate_exports(command(['/usr/bin/nm', '-gUj', str(library)], env), fixture)
    validate_weak_link(command(['/usr/bin/otool', '-l', str(library)], env))


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported fixture invocation')
    arch = {'arm64': 'arm64', 'x86_64': 'x86_64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported architecture')
    env = {key: value for key, value in os.environ.items() if key in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR')}
    rust = shutil.which('rustc', path=env.get('PATH'))
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    if rust is None or not Path(swift).is_file() or not Path(sdk).is_dir():
        raise ValueError('existing compiler unavailable')
    # Explicit fixture target only; never alter the app deployment target.
    env.update(MACOSX_DEPLOYMENT_TARGET='11.0', SDKROOT=sdk)
    with tempfile.TemporaryDirectory(prefix='resident-rust-owner-') as temporary:
        root = Path(temporary)
        passed = 0
        for target_arch in (arch, 'x86_64' if arch == 'arm64' else 'arm64'):
            for fixture in (False, True):
                directory = root / (target_arch + ('-fixture' if fixture else '-normal'))
                directory.mkdir()
                build_swift(swift, sdk, target_arch, fixture, directory, root / ('cache-' + target_arch), env)
                if target_arch == arch:
                    passed += rust_variant(rust, directory, fixture, env)
    print(json.dumps({'fixture': 'resident-rust-owner', 'passed': passed, 'compileRefusals': 9,
                      'swiftCompileTargets': 2, 'rustCompileTargets': 1, 'variants': ['normal', 'fixture'],
                      'foreignRustCompile': False, 'foreignExecution': False, 'producerLaunches': 0,
                      'scope': 'unlaunched owner and owned anonymous IO; no active EOF / Wait / joins proof'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_RUST_OWNER_FIXTURE_FAILED', file=sys.stderr)
        raise SystemExit(1)
