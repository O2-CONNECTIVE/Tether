"""Fixed inputs and compiler commands for the nonexecuting hosted GUI candidate."""
import json
import os
from pathlib import Path, PurePosixPath
import re

import build_mac_resident_source as source
import build_mac_sealed_producer as producer
import verify_mac_gui_link_layout as layout

TARGET = 'aarch64-apple-darwin'
MINIMUM = '14.4'
APP = 'Tether-CI-Candidate.app'
NATIVE_ROOT = 'ui/tauri/native/resident-launch'
SWIFT_FILES = ('ResidentLaunchPreparation.swift', 'OwnedPreparationIO.swift',
               'ResidentPreparationABI.swift', 'PreparedByteIO.swift')
EXPORTS = {'_rh_resident_preparation_' + name for name in (
    'create', 'startup', 'launch', 'state', 'retire', 'read', 'write', 'half_close', 'exit',
    'retire_active_io', 'abandon_active', 'request_active_close',
    'discard_observation', 'finish_active')}
RECIPE = ('scripts/build_mac_gui_candidate.py', 'scripts/mac_gui_build_support.py',
          'scripts/build_mac_resident_source.py', 'scripts/go_ci.py',
          'scripts/verify_mac_gui_link_layout.py', 'scripts/package_mac_agent.py',
          'scripts/build_mac_sealed_producer.py', 'scripts/collect_go_licenses.py')
CARGO_FLAGS = ('--locked', '--offline', '--release', '--jobs', '2',
               '--target', TARGET, '--bin', 'rescue-host-shell',
               '--features', 'custom-protocol')
BLOCKERS = ('RELEASE_IDENTITY_UNCONFIGURED', 'SIGNING_UNVERIFIED',
            'ENROLLMENT_UNCONFIGURED', 'SIGNED_PACKAGE_STARTUP_UNVERIFIED', 'PLACEMENT_UNVERIFIED',
            'VERIFY_TO_EXEC_UNVERIFIED', 'RUNTIME_COMPATIBILITY_UNVERIFIED',
            'TOOLCHAIN_SDK_INTEGRITY_UNVERIFIED', 'NOTICE_CLOSURE_UNAUDITED',
            'BINARY_SECRET_PATH_AUDIT_PENDING')


def snapshot(revision, stage):
    """Preserve source-relative imports, never copy ignored local build products."""
    entries = source.git('ls-tree', '-r', '-z', revision, '--', 'ui/tauri').split(b'\0')
    files = {}
    for entry in entries:
        if not entry:
            continue
        metadata, raw_name = entry.split(b'\t', 1)
        name = raw_name.decode('ascii')
        parts = PurePosixPath(name).parts
        source.require(metadata.split()[0] in (b'100644', b'100755') and
                       re.fullmatch(r'[A-Za-z0-9_./-]+', name) and
                       parts[:2] == ('ui', 'tauri') and '..' not in parts and
                       not set(parts) & {'node_modules', 'target', 'dist', '.cargo', '.npmrc'},
                       'GUI_SOURCE_INVALID')
        data = source.source_blob(revision, name)
        destination = stage / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(data)
        files[name] = source.digest(data)
    required = {'ui/tauri/package-lock.json', 'ui/tauri/src-tauri/Cargo.lock',
                'ui/tauri/src-tauri/tauri.conf.json', 'ui/tauri/src-tauri/src/main.rs',
                *(NATIVE_ROOT + '/' + name for name in (*SWIFT_FILES, 'ResidentPreparation.h'))}
    source.require(required <= files.keys(), 'GUI_SOURCE_INCOMPLETE')
    recipe = {name: source.digest(source.source_blob(revision, name)) for name in RECIPE}
    for name, expected in recipe.items():
        source.require(source.digest((source.ROOT / name).read_bytes()) == expected, 'RECIPE_CHANGED')
    return files, recipe


def environment(stage):
    # Only tool discovery and prepopulated public dependency caches are inherited.
    allowed = ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR', 'CARGO_HOME', 'RUSTUP_HOME')
    env = {key: os.environ[key] for key in allowed if key in os.environ}
    env.update(CARGO_TARGET_DIR=str(stage / 'cargo-target'), CARGO_NET_OFFLINE='true',
               MACOSX_DEPLOYMENT_TARGET=MINIMUM, CARGO_INCREMENTAL='0',
               npm_config_offline='true', npm_config_ignore_scripts='true',
               npm_config_audit='false', npm_config_fund='false',
               npm_config_userconfig=str(stage / 'empty-npmrc'),
               npm_config_globalconfig=str(stage / 'empty-global-npmrc'))
    (stage / 'empty-npmrc').write_bytes(b'')
    (stage / 'empty-global-npmrc').write_bytes(b'')
    return env


def toolchain(stage, env):
    # Cargo searches cwd ancestors as well as CARGO_HOME, not just the manifest.
    cargo_home = Path(env.get('CARGO_HOME', str(Path(env['HOME']) / '.cargo')))
    folders = {cargo_home, *(parent / '.cargo' for parent in (stage, *stage.parents))}
    source.require(not any(folder.is_symlink() or any(os.path.lexists(folder / name)
                   for name in ('config', 'config.toml')) for folder in folders),
                   'GUI_CARGO_CONFIG_UNSUPPORTED')
    def run(args):
        return source.command(args, stage, env).decode('utf-8').strip()
    source.require(run(['node', '--version']) == 'v24.22.1', 'GUI_TOOLCHAIN_MISMATCH')
    rust = run(['rustc', '--version'])
    source.require(rust.startswith('rustc 1.99.1 '), 'GUI_TOOLCHAIN_MISMATCH')
    xcode = run(['/usr/bin/xcodebuild', '-version'])
    source.require(xcode.splitlines()[0] == 'Xcode 27.1', 'GUI_TOOLCHAIN_MISMATCH')
    swift = run(['/usr/bin/xcrun', '--find', 'swiftc'])
    sdk = run(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'])
    source.require(Path(swift).is_file() and Path(sdk).is_dir(), 'GUI_TOOLCHAIN_MISSING')
    sdk_version = run(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-version'])
    source.require(re.fullmatch(r'27(?:\.\d+){1,2}', sdk_version), 'GUI_TOOLCHAIN_MISMATCH')
    env['SDKROOT'] = sdk
    return swift, sdk, {'node': '24.22.1', 'rust': rust, 'xcode': xcode,
                        'swift': run([swift, '--version']), 'sdk': sdk_version,
                        'deploymentTarget': MINIMUM}


def compile_images(stage, env, swift, sdk, *, signed_release=False):
    frontend = stage / 'ui/tauri'
    def run(args, cwd, phase):
        try:
            return source.command(args, cwd, env)
        except source.BuildError:
            raise source.BuildError('GUI_' + phase + '_FAILED') from None
    run(['npm', 'ci', '--offline', '--ignore-scripts', '--no-audit', '--no-fund'], frontend, 'NPM_INSTALL')
    run(['npm', 'run', 'build'], frontend, 'FRONTEND_BUILD')
    run(['node', 'scripts/notices.mjs', '--check'], frontend, 'FRONTEND_NOTICE')
    native = stage / NATIVE_ROOT
    library = stage / 'libResidentPreparation.dylib'
    minimum = '27.0' if signed_release else MINIMUM
    swift_flags = ['-swift-version', '6', '-warnings-as-errors', '-sdk', sdk,
                   '-target', 'arm64-apple-macosx' + minimum, '-parse-as-library',
                   '-module-cache-path', str(stage / 'swift-cache'),
                   '-import-objc-header', str(native / 'ResidentPreparation.h'),
                   '-no-toolchain-stdlib-rpath', '-emit-library',
                   '-module-name', 'ResidentPreparation']
    selected = [str(native / name) for name in SWIFT_FILES]
    if signed_release:
        swift_flags += ['-D', 'TETHER_SIGNED_RELEASE']
        selected += [str(stage / 'generated/SignedResidentReleaseIdentity.swift')]
    run([swift, *swift_flags, *selected,
                    '-Xlinker', '-weak_framework', '-Xlinker', 'LightweightCodeRequirements',
                    '-Xlinker', '-install_name', '-Xlinker', layout.NATIVE,
                    '-Xlinker', '-rpath', '-Xlinker', layout.SWIFT_ROOT,
                    '-o', str(library)], stage, 'SWIFT_BUILD')
    symbols = run(['/usr/bin/nm', '-gUj', str(library)], stage, 'NATIVE_EXPORTS').decode().splitlines()
    source.require({line.strip() for line in symbols if line.startswith('_rh_')} == EXPORTS,
                   'GUI_NATIVE_EXPORTS_INVALID')
    # --target keeps native flags away from host build-script executables.
    # Preserve the same required native image used by the closed startup ABI.
    flags = ['--cfg', 'resident_native_source', '-Dwarnings',
             '-C', 'link-arg=-Wl,-needed_library,' + str(library),
             '-C', 'link-arg=-Wl,-rpath,' + layout.FRAMEWORKS]
    env['CARGO_ENCODED_RUSTFLAGS'] = '\x1f'.join(flags)
    run(['cargo', 'build', *CARGO_FLAGS, '--manifest-path',
         str(frontend / 'src-tauri/Cargo.toml')], stage, 'CARGO_BUILD')
    return {'gui': stage / ('cargo-target/' + TARGET + '/release/rescue-host-shell'),
            'native': library}


def plist_config(stage, minimum):
    config = json.loads((stage / 'ui/tauri/src-tauri/tauri.conf.json').read_text())
    source.require(config['identifier'] == 'com.o2connective.rescue-host.shell' and
                   config['version'] == '0.1.0' and config['bundle']['active'] is False,
                   'GUI_BUNDLE_CONFIG_CHANGED')
    return {'CFBundleExecutable': 'rescue-host-shell', 'CFBundleIdentifier': config['identifier'],
            'CFBundleName': 'Tether CI Candidate', 'CFBundlePackageType': 'APPL',
            'CFBundleShortVersionString': config['version'], 'CFBundleVersion': config['version'],
            'LSMinimumSystemVersion': minimum, 'NSHighResolutionCapable': True}


def build_producer(stage, revision, tree):
    # Build the existing sealed command ourselves; no imported binary, path or hash.
    output = stage / 'sealed-producer'
    record = producer.build(output)
    source.require(record['sourceCommit'] == revision and record['sourceTree'] == tree and
                   record['target'] == 'darwin-arm64' and record['role'] == producer.ROLE,
                   'PRODUCER_SOURCE_MISMATCH')
    path = output / 'resident-pipe'
    source.require(producer.inspect_image(path) == record['image'], 'PRODUCER_IMAGE_MISMATCH')
    return path, record
