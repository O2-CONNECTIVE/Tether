"""Build only the fixed native source crate; no launcher or command input."""
from dataclasses import dataclass
import os
from pathlib import Path
import platform
import shutil
import tempfile

from check_mac_enrollment_lookup import command
from check_mac_native_pump import CRATE, validate_dependencies
from check_mac_rust_owner import build_swift


@dataclass(frozen=True)
class SourceVariant:
    fixture: bool
    cargo: str
    executable: Path
    manifest: list[str]
    env: dict[str, str]


def source_variants(prefix):
    validate_dependencies()
    arch = {'arm64': 'arm64', 'x86_64': 'x86_64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported native source architecture')
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR', 'CARGO_HOME')}
    cargo = shutil.which('cargo', path=env.get('PATH'))
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    if cargo is None or not Path(swift).is_file() or not Path(sdk).is_dir():
        raise ValueError('existing native compiler unavailable')
    env.update(MACOSX_DEPLOYMENT_TARGET='11.0', SDKROOT=sdk)
    with tempfile.TemporaryDirectory(prefix=prefix) as temporary:
        root = Path(temporary)
        for fixture in (False, True):
            directory = root / ('fixture' if fixture else 'normal')
            directory.mkdir()
            build_swift(swift, sdk, arch, fixture, directory, root / 'swift-cache', env)
            variant = dict(env, CARGO_TARGET_DIR=str(directory / 'target'))
            flags = ['-Dwarnings', '-L', 'native=' + str(directory), '-l', 'dylib=ResidentPreparation',
                     '-C', 'link-arg=-Wl,-rpath,' + str(directory), '--cfg', 'resident_native_source']
            if fixture:
                flags += ['--cfg', 'resident_launch_fixture']
            variant['CARGO_ENCODED_RUSTFLAGS'] = '\x1f'.join(flags)
            manifest = ['--manifest-path', str(CRATE / 'Cargo.toml')]
            command([cargo, 'build', '--locked', '--offline', '--jobs', '2'] + manifest, variant)
            command([cargo, 'clippy', '--locked', '--offline', '--jobs', '2'] + manifest + ['--', '-Dwarnings'], variant)
            yield SourceVariant(fixture, cargo, directory / 'target/debug/native-pump-source-fixture', manifest, variant)
