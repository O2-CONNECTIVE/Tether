"""Compile/link real native source for Intel; never execute a foreign image."""
import json
import os
from pathlib import Path
import platform
import shutil
import sys
import tempfile

from check_mac_enrollment_lookup import command, completed
from check_mac_native_pump import CRATE, validate_dependencies
from check_mac_rust_owner import NATIVE, build_swift, validate_rejection

TARGET = 'x86_64-apple-darwin'


def require_intel_std(directory):
    root = Path(directory.strip())
    if not root.is_dir() or not any(root.glob('libstd-*.rlib')):
        raise ValueError('existing Intel Rust std unavailable')


def require_intel_image(path, env):
    if command(['/usr/bin/lipo', '-archs', str(path)], env).strip() != 'x86_64':
        raise ValueError('linked image is not Intel-only')


def compile_owner(rust, directory, fixture, env):
    base = [rust, '--edition=2024', '--target', TARGET, '-Dwarnings']
    if fixture:
        base += ['--cfg', 'resident_launch_fixture']
    library = directory / 'libresident_preparation.rlib'
    command(base + ['--crate-type=rlib', '--crate-name', 'resident_preparation',
                    str(NATIVE / 'rust/owner.rs'), '-o', str(library)], env)
    external = ['--extern', 'resident_preparation=' + str(library)]
    executable = directory / 'rust-owner-fixture'
    command(base + [str(NATIVE / 'fixtures/RustDriver.rs')] + external +
            ['-L', 'native=' + str(directory), '-l', 'dylib=ResidentPreparation',
             '-C', 'link-arg=-Wl,-rpath,@executable_path', '-o', str(executable)], env)
    require_intel_image(executable, env)
    refusals = [('NotSend', 'E0277', 'cannot be sent'), ('NotSync', 'E0277', 'cannot be shared'),
                ('BorrowRetire', 'E0499', 'more than once'),
                ('BorrowDrop', 'E0505', 'because it is borrowed')]
    if not fixture:
        refusals += [('NoNormalFixture', 'E0599', 'fixture')]
    for name, code, message in refusals:
        result = completed(base + [str(NATIVE / ('fixtures/' + name + '.rs'))] + external +
                           ['--emit=metadata', '--error-format=json', '-o', str(directory / (name + '.rmeta'))], env)
        validate_rejection(result, code, message)
    return len(refusals)


def compile_pump(cargo, directory, fixture, env):
    variant = dict(env, CARGO_TARGET_DIR=str(directory / 'target'))
    flags = ['-Dwarnings', '-L', 'native=' + str(directory), '-l', 'dylib=ResidentPreparation',
             '-C', 'link-arg=-Wl,-rpath,' + str(directory)]
    if fixture:
        flags += ['--cfg', 'resident_native_source', '--cfg', 'resident_launch_fixture']
    variant['CARGO_ENCODED_RUSTFLAGS'] = '\x1f'.join(flags)
    base = ['--locked', '--offline', '--jobs', '2', '--target', TARGET,
            '--manifest-path', str(CRATE / 'Cargo.toml')]
    command([cargo, 'build'] + base, variant)
    command([cargo, 'clippy'] + base + ['--', '-Dwarnings'], variant)
    require_intel_image(directory / ('target/' + TARGET + '/debug/native-pump-source-fixture'), env)


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin' or platform.machine() != 'arm64':
        raise ValueError('Intel cross fixture requires an existing arm64 Mac toolchain')
    validate_dependencies()
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR', 'CARGO_HOME')}
    rust, cargo = (shutil.which(name, path=env.get('PATH')) for name in ('rustc', 'cargo'))
    if rust is None or cargo is None:
        raise ValueError('existing Rust tools unavailable')
    if not command([rust, '--version'], env).startswith('rustc 1.99.1 '):
        raise ValueError('unreviewed Rust version')
    require_intel_std(command([rust, '--print', 'target-libdir', '--target', TARGET], env))
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    env.update(MACOSX_DEPLOYMENT_TARGET='11.0', SDKROOT=sdk)  # Fixture only.
    refused = 0
    with tempfile.TemporaryDirectory(prefix='native-intel-source-') as temporary:
        root = Path(temporary)
        for fixture in (False, True):
            directory = root / ('fixture' if fixture else 'normal')
            directory.mkdir()
            build_swift(swift, sdk, 'x86_64', fixture, directory, root / 'swift-cache', env)
            require_intel_image(directory / 'libResidentPreparation.dylib', env)
            refused += compile_owner(rust, directory, fixture, env)
            compile_pump(cargo, directory, fixture, env)
    print(json.dumps({'fixture': 'native-intel-source', 'target': TARGET,
                      'ownerLinkedVariants': 2, 'pumpLinkedVariants': 2, 'clippyVariants': 2,
                      'compileRefusals': refused, 'foreignExecution': False,
                      'producerLaunches': 0, 'scope': 'compile/link only; no Intel runtime or Process Wait proof'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_INTEL_SOURCE_FAILED', file=sys.stderr)
        raise SystemExit(1)
