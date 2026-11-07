"""Build a blocked local Mac evaluation archive; never run, enroll or distribute."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import struct
import zipfile

try:
    from scripts import collect_go_licenses as licenses
except ModuleNotFoundError:
    import collect_go_licenses as licenses

ROOT = Path(__file__).resolve().parent.parent
BINARY_LIMIT = 64 * 1024 * 1024
TEXT_LIMIT = 16 * 1024
TEXTS = {
    'go-toolchain/go1.27.3/LICENSE': '911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad',
    'go-toolchain/go1.27.3/PATENTS': '96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc',
    'crypto-v0.58.0/LICENSE': '911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad',
    'crypto-v0.58.0/PATENTS': '96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc',
    'sys-v0.49.0/LICENSE': '911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad',
    'sys-v0.49.0/PATENTS': '96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc',
    'websocket-v1.8.15/LICENSE.txt': 'cc0975a5f6305145bdd7b41ce9479632fdac3870e6ac4281f28017f18c767c4e',
}
TARGETS = {'darwin-amd64': 0x01000007, 'darwin-arm64': 0x0100000c}
INSTRUCTIONS = """Tether Mac local evaluation candidate — DISTRIBUTION BLOCKED

This unsigned archive is not an installer or an authenticated publisher release.
A SHA256 match detects byte differences, not publisher identity or runtime safety.
The supplied source-manifest hash is a provenance label only: binary-to-source
correspondence, reproducible build, linked dependency closure and complete notices
remain unverified. Embedded secrets/build paths have not been audited; this is
also a distribution blocker. Notarization/signature validity is not checked.
Fixed Go/direct-module license texts are NOT a complete legal
or distribution clearance. Optional --dependency-inventory checks only the fixed
collector record against this checkout's locks and the fixed notice identities.
The inventory source commit remains an unverified declaration. The input record
is not copied into the archive; its hash and unchecked scopes are recorded.
Do not distribute this candidate until those gaps close.

For an already enrolled Mac only, after separate explicit authorization: a human
may manually invoke rescue-mac-agent with -config and an existing protected config
path, in their own terminal. No config, key, token, enrollment or automatic launch
is supplied. First-time device access and enrollment are separate requirements.
Read the /dev/tty approval carefully and type its full fresh confirmation yourself.
Never automate consent or reuse an expired grant. Do not bypass macOS security,
quarantine or signing checks. No administrator rights or persistent service needed.

The grant permits commands as the current Mac user: NOT a sandbox or printf-only
permission. Compromise can affect files accessible to that user. Expiry/revoke and
transport loss request owned cleanup; TERMINAL_CLEANUP_UNKNOWN means cleanup was
not confirmed and related processes may remain. Do not report success or retry a
command whose start/result is uncertain. Stop and preserve nonsecret diagnostics.
Do not disable safety checks, kill unrelated processes or retry an old request with
a new ID. Revoking access does not undo already executed changes or copied data.

Modern macOS LC_BUILD_VERSION is required; legacy version commands are refused.
Minimum OS is a binary header declaration, not a compatibility test. SDK/build-tool
versions are build metadata, not software the recipient must install.
Nothing in this package grants access, creates credentials or authorizes execution.
"""


class PackageError(Exception):
    pass


def require(ok, code):
    if not ok:
        raise PackageError(code)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def directory_chain(path):
    path = Path(os.path.abspath(path))
    for part in (path, *path.parents):
        s = part.lstat()
        require(stat.S_ISDIR(s.st_mode) and not getattr(s, 'st_file_attributes', 0) & 1024,
                'DIRECTORY_INVALID')


def read_regular(path, limit):
    path = Path(path)
    directory_chain(path.parent)
    before = path.lstat()
    require(stat.S_ISREG(before.st_mode) and not getattr(before, 'st_file_attributes', 0) & 1024,
            'FILE_INVALID')
    require(0 < before.st_size <= limit, 'SIZE_INVALID')
    flags = os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0) | getattr(os, 'O_NONBLOCK', 0)
    with os.fdopen(os.open(path, flags), 'rb') as stream:
        opened = os.fstat(stream.fileno())
        require(stat.S_ISREG(opened.st_mode) and (opened.st_dev, opened.st_ino) ==
                (before.st_dev, before.st_ino), 'FILE_CHANGED')
        data = stream.read(limit + 1)
        after = os.fstat(stream.fileno())
    require(0 < len(data) <= limit and len(data) == before.st_size == after.st_size and
            opened.st_mtime_ns == after.st_mtime_ns, 'FILE_CHANGED')
    return data


def macho(data, target):
    require(target in TARGETS and len(data) >= 32, 'MACHO_INVALID')
    magic, cpu, subtype, kind, count, size, flags, reserved = struct.unpack('<8I', data[:32])
    require(magic == 0xfeedfacf and cpu == TARGETS[target] and kind == 2 and
            size <= len(data) - 32 and count <= size // 8, 'MACHO_INVALID')
    offset, end, build = 32, 32 + size, None
    for _ in range(count):
        require(offset + 8 <= end, 'LOAD_COMMAND_INVALID')
        command, length = struct.unpack_from('<2I', data, offset)
        require(length >= 8 and length % 8 == 0 and length <= end - offset,
                'LOAD_COMMAND_INVALID')
        # This evaluator supports modern macOS only, not legacy minimum-OS commands.
        require(command not in (0x24, 0x25, 0x2f, 0x30), 'LEGACY_PLATFORM_UNSUPPORTED')
        if command == 0x32:  # LC_BUILD_VERSION
            require(build is None and length >= 24, 'BUILD_VERSION_INVALID')
            platform, minimum, sdk, tools = struct.unpack_from('<4I', data, offset + 8)
            require(platform == 1 and 0 < minimum <= sdk and
                    length == 24 + tools * 8, 'BUILD_VERSION_INVALID')
            versions = {}
            for index in range(tools):
                tool, version = struct.unpack_from('<2I', data, offset + 24 + index * 8)
                require(tool in (1, 2, 3) and tool not in versions and version > 0,
                        'BUILD_TOOL_INVALID')
                versions[tool] = version_string(version)
            build = {'minimumMacOSDeclared': version_string(minimum),
                     'sdkDeclared': version_string(sdk),
                     'buildToolsDeclared': {str(k): v for k, v in sorted(versions.items())},
                     'compatibilityVerified': False}
        offset += length
    require(offset == end and build is not None, 'BUILD_VERSION_MISSING_OR_INCONSISTENT')
    return build


def version_string(value):
    return f'{value >> 16}.{(value >> 8) & 255}.{value & 255}'


def dependency_audit(path):
    # A supplied collector record is checked, never treated as build evidence.
    audit = {'inventoryStatus': 'not-supplied',
             'scope': 'fixed-direct-modules-and-toolchain-texts-only',
             'unverifiedScopes': [
                 'binary-source-correspondence', 'linked-module-closure',
                 'stdlib-vendored-notices', 'platform-runtime-notices',
                 'toolchain-provenance', 'build-tool-dependencies']}
    if path is None:
        return audit
    data = read_regular(path, 64 * 1024)
    try:
        record = json.loads(data, object_pairs_hook=licenses.strict_object)
        require(isinstance(record, dict), 'DEPENDENCY_INVENTORY_INVALID')
        source = record.get('sourceCommit')
        require(isinstance(source, str) and re.fullmatch('[0-9a-f]{40}', source),
                'DEPENDENCY_SOURCE_INVALID')
        locks = {name: read_regular(ROOT / 'backend' / name, TEXT_LIMIT)
                 for name in ('go.mod', 'go.sum')}
        licenses.check_locks(locks)
        expected = licenses.inventory_record(source, locks)
        # Canonical JSON distinguishes booleans from 0/1, unlike Python equality.
        require(json.dumps(record, sort_keys=True) == json.dumps(expected, sort_keys=True),
                'DEPENDENCY_INVENTORY_MISMATCH')
        declared = {item['path']: item['sha256'] for component in
                    [record['toolchain'], *record['modules']]
                    for item in component['texts'].values()}
        require(declared == TEXTS, 'DEPENDENCY_NOTICE_SET_MISMATCH')
    except licenses.CollectionError as error:
        raise PackageError('DEPENDENCY_INVENTORY_INVALID') from error
    audit.update(inventoryStatus='fixed-record-matched', inventorySha256=sha(data))
    return audit


def package(binary, expected_sha256, target, source_manifest_sha256, notices_root, output, dependency_inventory=None):
    require(bool(re.fullmatch('[0-9a-f]{64}', expected_sha256)) and
            bool(re.fullmatch('[0-9a-f]{64}', source_manifest_sha256)), 'HASH_INVALID')
    output = Path(output)
    directory_chain(output.parent)
    require(not os.path.lexists(output), 'OUTPUT_EXISTS')
    data = read_regular(binary, BINARY_LIMIT)
    require(sha(data) == expected_sha256, 'BINARY_HASH_MISMATCH')
    platform = macho(data, target)
    audit = dependency_audit(dependency_inventory)
    files = {'rescue-mac-agent': data, 'LICENSE': read_regular(ROOT / 'LICENSE', TEXT_LIMIT),
             'RECIPIENT.txt': INSTRUCTIONS.encode('utf-8')}
    # Fixed files only; never enumerate or copy unrelated content in this root.
    for name, digest in TEXTS.items():
        text = read_regular(Path(notices_root) / name, TEXT_LIMIT)
        require(sha(text) == digest, 'NOTICE_HASH_MISMATCH')
        files['notices/' + name] = text
    manifest = {'schema': 1, 'target': target, 'signingStatus': 'unsigned-unverified',
                'distributionStatus': 'blocked', 'binarySourceCorrespondenceVerified': False,
                'binaryDependenciesVerified': False, 'noticeCompletenessVerified': False, 'binarySecretAuditVerified': False,
                'notarizationVerified': False, 'dependencyAudit': audit,
                'sourceManifestSha256Supplied': source_manifest_sha256,
                'platformDeclaration': platform,
                'files': {name: sha(value) for name, value in sorted(files.items())}}
    files['MANIFEST.json'] = (json.dumps(manifest, sort_keys=True, indent=2) + '\n').encode()
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, 'w', compression=zipfile.ZIP_STORED) as archive:
        for name, value in sorted(files.items()):
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.create_system = 3
            info.external_attr = (stat.S_IFREG | (0o555 if name == 'rescue-mac-agent' else 0o444)) << 16
            archive.writestr(info, value)
    payload = buffer.getvalue()
    # Exclusive destination. A failed write can leave an incomplete file; never
    # overwrite/remove it. Ancestor concurrent replacement is not sandboxed.
    fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(payload)
    return {'status': 'local_archive_created', 'distributionStatus': 'blocked',
            'archiveSha256': sha(payload), 'binarySha256': expected_sha256}


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise PackageError('ARGUMENT_INVALID')


def main(argv=None):
    try:
        parser = Parser(description=__doc__)
        for option in ('binary', 'expected-sha256', 'target', 'source-manifest-sha256', 'notices-root', 'output'):
            parser.add_argument('--' + option, required=True)
        parser.add_argument('--dependency-inventory', help='Optional fixed collector DEPENDENCIES.json; does not verify closure')
        args = parser.parse_args(argv)
        result = package(**vars(args))
    except PackageError as error:
        print(json.dumps({'status': 'refused', 'code': str(error)}))
        return 1
    except (OSError, ValueError, TypeError, RecursionError):
        print(json.dumps({'status': 'refused', 'code': 'IO_OR_INPUT_INVALID'}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
