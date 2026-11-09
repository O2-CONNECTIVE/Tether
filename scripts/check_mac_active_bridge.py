"""Link real C ABI/Rust active observations; only fixed untrusted self-children."""
import json
import os
from pathlib import Path
import platform
import re
import shutil
import sys
import tempfile

from check_mac_enrollment_lookup import command, completed
from check_mac_rust_owner import NATIVE, build_swift, validate_rejection

CASES = {'echo': 33, 'epipe': 34, 'missing': 31, 'lost': 30, 'exit-two': 32,
         'signal': 32, 'terminal': 26, 'drop': 9, 'unwind': 10, 'thread-exit': 9, 'guards': 10, 'decoder': 15}
NO_CHILD = {'normal', 'guards', 'decoder'}


def validate_report(mode, output):
    expected = 5 if mode == 'normal' else CASES.get(mode)
    child = int(mode not in NO_CHILD)
    unknown = int(child and mode != "terminal")
    match = re.fullmatch(r'NATIVE_ACTIVE_BRIDGE mode=([a-z-]+) passed=(\d+) failed=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) open=(\d+) unknown=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, child, child, 0, unknown, 0):
        raise ValueError('incomplete or uncollected active owner fixture')
    return expected


def active_variant(rust, directory, fixture, env, target=None, execute=True):
    flags = ['--edition=2024', '-Dwarnings']
    if target:
        flags += ['--target', target]
    if fixture:
        flags += ['--cfg', 'resident_launch_fixture']
    external = ['--extern', 'resident_preparation=' + str(directory / 'libresident_preparation.rlib')]
    executable = directory / 'active-bridge-fixture'
    command([rust] + flags + [str(NATIVE / 'fixtures/ActiveRustDriver.rs')] + external +
            ['-L', 'native=' + str(directory), '-l', 'dylib=ResidentPreparation',
             '-C', 'link-arg=-Wl,-rpath,@executable_path', '-o', str(executable)], env)
    passed = 0
    if execute:
        for mode in CASES if fixture else ('normal',):
            passed += validate_report(mode, command([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'}))
    negatives = [('BorrowActiveRetire', 'E0499', 'more than once'), ('BorrowActivePoll', 'E0499', 'more than once'),
                 ('BorrowActiveClose', 'E0499', 'more than once'),
                 ('BorrowActiveDiscard', 'E0499', 'more than once'),
                 ('BorrowActiveFinish', 'E0499', 'more than once'),
                 ('NoCopyTerminal', 'E0277', 'Copy')]
    if not fixture:
        negatives += [('NoNormalActiveFixture', 'E0599', 'fixture_active')]
    for name, code, message in negatives:
        result = completed([rust] + flags + [str(NATIVE / ('fixtures/' + name + '.rs'))] + external +
                           ['--emit=metadata', '--error-format=json', '-o', str(directory / (name + '.rmeta'))], env)
        validate_rejection(result, code, message)
    return passed, len(negatives), executable



def validate_embedding(rust, directory, fixture, env):
    # Reproduce the real nested source consumer, where active observations are
    # deliberately not wired yet. This is metadata only: no actor is launched.
    for edition in ('2021', '2024'):
        flags = ['--edition=' + edition, '-Dwarnings'] + (['--cfg', 'resident_launch_fixture'] if fixture else [])
        command([rust] + flags + [str(NATIVE / 'fixtures/EmbeddedOwner.rs'), '--emit=metadata',
                '-o', str(directory / ('embedded-' + edition + '.rmeta'))], env)


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported active bridge invocation')
    arch = {'arm64': 'arm64', 'x86_64': 'x86_64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported active bridge architecture')
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR')}
    rust = shutil.which('rustc', path=env.get('PATH'))
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    if rust is None or not Path(swift).is_file() or not Path(sdk).is_dir():
        raise ValueError('existing compiler unavailable')
    env.update(MACOSX_DEPLOYMENT_TARGET='11.0', SDKROOT=sdk)
    passed = refused = 0
    with tempfile.TemporaryDirectory(prefix='resident-active-bridge-') as temporary:
        root = Path(temporary)
        for target_arch in (arch, 'x86_64' if arch == 'arm64' else 'arm64'):
            for fixture in (False, True):
                directory = root / (target_arch + ('-fixture' if fixture else '-normal'))
                directory.mkdir()
                build_swift(swift, sdk, target_arch, fixture, directory, root / ('cache-' + target_arch), env)
                if target_arch == arch:
                    flags = ['--edition=2024', '-Dwarnings'] + (['--cfg', 'resident_launch_fixture'] if fixture else [])
                    command([rust] + flags + ['--crate-type=rlib', '--crate-name', 'resident_preparation',
                            str(NATIVE / 'rust/owner.rs'), '-o', str(directory / 'libresident_preparation.rlib')], env)
                    validate_embedding(rust, directory, fixture, env)
                    count, negatives, _ = active_variant(rust, directory, fixture, env)
                    passed += count
                    refused += negatives
    print(json.dumps({'fixture': 'native-active-component-bridge', 'passed': passed,
                      'fixtureChildrenStarted': 10, 'fixtureChildrenWaited': 10, 'physicalOriginalEndpointsClosed': 40,
                      'compileRefusals': refused, 'embeddedMetadataVariants': 4, 'swiftCompileTargets': 2, 'rustCompileTargets': 1,
                      'foreignExecution': False, 'producerLaunches': 0,
                      'scope': 'same native record exit/IO components; no signed launch / full joins / replacement permission'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_ACTIVE_BRIDGE_FAILED', file=sys.stderr)
        raise SystemExit(1)
