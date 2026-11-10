"""Fixed signing and generated source for the local Mac release composition."""
import json
from pathlib import Path
import plistlib
import re
import stat

import mac_gui_build_support as support
from mac_gui_build_support import source, layout

PROVIDER = 'generated/SignedResidentReleaseIdentity.swift'
OBSERVER = 'scripts/mac_release_signature.swift'
IDENTIFIERS = {'producer': 'com.o2connective.rescue-host.resident-pipe',
               'native': 'com.o2connective.rescue-host.resident-preparation',
               'gui': 'com.o2connective.rescue-host.shell'}
RECIPE = (*support.RECIPE, 'scripts/build_mac_signed_release.py',
          'scripts/mac_signed_release_support.py', OBSERVER)
BLOCKERS = ('NOTARIZATION_PENDING', 'ENROLLMENT_UNCONFIGURED',
            'SIGNED_PACKAGE_STARTUP_UNVERIFIED', 'VERIFY_TO_EXEC_UNVERIFIED',
            'RUNTIME_COMPATIBILITY_UNVERIFIED', 'NOTICE_CLOSURE_UNAUDITED',
            'BINARY_SECRET_PATH_AUDIT_PENDING', 'TOOLCHAIN_SDK_INTEGRITY_UNVERIFIED')


def identity(team, certificate):
    source.require(isinstance(team, str) and re.fullmatch('[A-Z0-9]{10}', team) and
                   isinstance(certificate, str) and re.fullmatch('[0-9A-F]{40}', certificate) and
                   certificate != '0' * 40, 'SIGNING_IDENTITY_INVALID')


def snapshot(revision, stage):
    files, recipe = support.snapshot(revision, stage)
    for name in RECIPE:
        data = source.source_blob(revision, name)
        source.require(source.digest((source.ROOT / name).read_bytes()) == source.digest(data), 'RECIPE_CHANGED')
        recipe[name] = source.digest(data)
        if name == OBSERVER:
            path = stage / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
            files[name] = source.digest(data)
    return files, recipe


def compile_observer(stage, env, swift, sdk):
    executable = stage / 'signature-observer'
    source.command([swift, '-swift-version', '6', '-warnings-as-errors', '-sdk', sdk,
                    '-target', 'arm64-apple-macosx27.0', '-module-cache-path', str(stage / 'observer-cache'),
                    str(stage / OBSERVER), '-o', str(executable)], stage, env)
    return executable


def sign(path, role, certificate, stage, env):
    # Explicit inside-out signing. Never --deep, a key import, or entitlement expansion.
    source.command(['/usr/bin/codesign', '--force', '--sign', certificate,
                    '--identifier', IDENTIFIERS[role], '--options', 'runtime',
                    '--timestamp', '--generate-entitlement-der', str(path)], stage, env)


def observe(observer, path, role, team, certificate, stage, env, cdhash=None):
    args = [str(observer), '--path', str(path), '--team-id', team,
            '--identifier', IDENTIFIERS[role], '--certificate-sha1', certificate]
    if cdhash is not None:
        args += ['--cdhash', cdhash]
    return json.loads(source.command(args, stage, env))


def validate_observation(result, role, team, certificate, cdhash=None):
    expected = {'schema': 'tether-mac-release-signature/1', 'teamIdentifier': team,
                'signingIdentifier': IDENTIFIERS[role], 'certificateSha1': certificate,
                'validationCategory': 'developer-id', 'secureTimestamp': True,
                'hardenedRuntime': True, 'entitlements': {}}
    source.require(isinstance(result, dict) and set(result) == {*expected, 'cdhash'} and
                   all(type(result[key]) is type(value) and result[key] == value
                       for key, value in expected.items()), 'SIGNATURE_OBSERVATION_INVALID')
    value = result['cdhash']
    source.require(isinstance(value, str) and re.fullmatch('[0-9a-f]{40}', value) and
                   value != '0' * 40 and (cdhash is None or value == cdhash),
                   'SIGNATURE_OBSERVATION_INVALID')
    return result


def provider_bytes(observation):
    # Callers pass only a freshly validated observation, never a user manifest.
    team, value = observation['teamIdentifier'], observation['cdhash']
    source.require(re.fullmatch('[A-Z0-9]{10}', team) and
                   re.fullmatch('[0-9a-f]{40}', value) and value != '0' * 40,
                   'SIGNATURE_OBSERVATION_INVALID')
    octets = ', '.join('0x' + value[index:index + 2] for index in range(0, 40, 2))
    return ('import Foundation\n\n// Generated from this transaction\'s validated signed producer.\n'
            'enum SignedResidentReleaseIdentity {\n'
            f'    static let team = "{team}"\n'
            f'    static let hashes = [Data([{octets}])]\n'
            '}\n').encode('ascii')


def image_record(path, relative):
    data = layout.files.read_regular(path, layout.LIMIT)
    return {'path': relative, 'sha256': source.digest(data), 'bytes': len(data)}


def unchanged(path, expected):
    source.require(image_record(path, expected['path']) == expected, 'SIGNED_PRODUCER_CHANGED')


def verify_inputs(stage, files, revision, tree, recipe):
    for name, expected in files.items():
        source.require(source.digest(layout.files.read_regular(stage / name, layout.LIMIT)) == expected,
                       'BUILD_INPUT_CHANGED')
    for name, expected in recipe.items():
        source.require(source.digest((source.ROOT / name).read_bytes()) == expected, 'RECIPE_CHANGED')
    source.require(source.head() == (revision, tree), 'SOURCE_CHANGED')


def inventory(app, hashes):
    """Signed local-stage inventory. The inert candidate validator stays unchanged."""
    expected = {'': {'Contents'},
                'Contents': {'MacOS', 'Frameworks', 'Helpers', 'Info.plist', '_CodeSignature'},
                'Contents/MacOS': {'rescue-host-shell'},
                'Contents/Frameworks': {'libResidentPreparation.dylib'},
                'Contents/Helpers': {'resident-pipe'},
                'Contents/_CodeSignature': {'CodeResources'}}
    for relative, names in expected.items():
        directory = app / relative
        layout.files.directory_chain(directory)
        source.require({p.name for p in directory.iterdir()} == names, 'SIGNED_INVENTORY_INVALID')
    source.require(set(hashes) == set(layout.ASSEMBLY_IMAGES), 'SIGNED_INVENTORY_INVALID')
    minima = [27 << 16]
    for role, relative in layout.ASSEMBLY_IMAGES.items():
        path = app / relative
        data = layout.files.read_regular(path, layout.LIMIT)
        source.require(source.digest(data) == hashes[role] and stat.S_IMODE(path.stat().st_mode) == 0o700,
                       'SIGNED_IMAGE_CHANGED')
        minima.append(layout.inspect(data, 'darwin-arm64', role))
    plist = plistlib.loads(layout.files.read_regular(app / 'Contents/Info.plist', 64 * 1024))
    minimum = layout.files.version_string(max(minima))
    source.require(plist == signed_plist(minimum), 'SIGNED_PLIST_INVALID')
    seal = layout.files.read_regular(app / 'Contents/_CodeSignature/CodeResources', 4 * 1024 * 1024)
    source.require(isinstance(plistlib.loads(seal), dict), 'SIGNED_INVENTORY_INVALID')
    return {'minimumMacOS': minimum, 'infoPlistSha256': source.digest(plistlib.dumps(plist, sort_keys=True)),
            'codeResourcesSha256': source.digest(seal)}


def signed_plist(minimum):
    return {'CFBundleExecutable': 'rescue-host-shell', 'CFBundleIdentifier': IDENTIFIERS['gui'],
            'CFBundleName': 'Tether Signed Candidate', 'CFBundlePackageType': 'APPL',
            'CFBundleShortVersionString': '0.1.0', 'CFBundleVersion': '0.1.0',
            'LSMinimumSystemVersion': minimum, 'NSHighResolutionCapable': True}
