"""Finite CI-only transfer of the unchanged PR180 native binaries."""

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import zipfile

SOURCE = '16a1de9e48f8ac4559b81feb2ac7a1c71a02ecf4'
LIMIT = 128 * 1024 * 1024
ZIP_LIMIT = LIMIT - 1024 * 1024  # Leave room for the Actions single-file wrapper.
BINARY_PATHS = ('app/rescue-host-shell.exe', 'tools/tauri/bin/tauri-driver.exe',
                'tools/edge/msedgedriver.exe')


def digest(data):
    return hashlib.sha256(data).hexdigest()


def safe_name(name):
    path = PurePosixPath(name)
    if (not name or '\\' in name or ':' in name or path.is_absolute()
            or any(part in ('', '.', '..') for part in name.split('/'))):
        raise ValueError('REPRO_PATH_REJECTED')
    return path


def allowed(name):
    return (name in BINARY_PATHS or name in ('SOURCE-LICENSE.txt',
            'tools/driver-provenance.json', 'tools/tauri-driver.crate')
            or name.startswith('tools/tauri-driver-2.1.0/')
            or name.startswith('tools/edge/'))


def unique_object(pairs):
    obj = {}
    for key, value in pairs:
        if key in obj:
            raise ValueError('REPRO_DUPLICATE_FIELD')
        obj[key] = value
    return obj


def read_manifest(raw, expected):
    if len(raw) > 65536 or digest(raw) != expected:
        raise ValueError('REPRO_MANIFEST_HASH_REJECTED')
    value = json.loads(raw, object_pairs_hook=unique_object)
    if (set(value) != {'schema', 'purpose', 'sourceCommit', 'workflowCommit',
                      'profile', 'features', 'distribution', 'files', 'payloadBytes'}
            or value['schema'] != 1 or value['purpose'] != 'pr180-finite-reproduction'
            or value['sourceCommit'] != SOURCE or value['profile'] != 'debug'
            or value['features'] != ['custom-protocol'] or value['distribution'] is not False
            or len(value['workflowCommit']) != 40
            or any(c not in '0123456789abcdef' for c in value['workflowCommit'])
            or not isinstance(value['files'], list) or not 3 <= len(value['files']) <= 256):
        raise ValueError('REPRO_MANIFEST_REJECTED')
    names = set()
    total = 0
    for row in value['files']:
        if set(row) != {'path', 'bytes', 'sha256'}:
            raise ValueError('REPRO_FILE_RECORD_REJECTED')
        name = row['path']
        safe_name(name)
        if not allowed(name) or name in names or type(row['bytes']) is not int or not 0 <= row['bytes'] <= LIMIT:
            raise ValueError('REPRO_FILE_RECORD_REJECTED')
        if len(row['sha256']) != 64 or any(c not in '0123456789abcdef' for c in row['sha256']):
            raise ValueError('REPRO_FILE_HASH_REJECTED')
        names.add(name)
        total += row['bytes']
    if not set(BINARY_PATHS).issubset(names) or total != value['payloadBytes'] or total + len(raw) > LIMIT:
        raise ValueError('REPRO_PAYLOAD_BOUND_REJECTED')
    return value


def runtime_file(name):
    return (name in {'tools/driver.stdout.log', 'tools/driver.stderr.log',
                    'tools/test.stdout.log', 'tools/test.stderr.log',
                    'tools/driver-stage.jsonl', 'tools/driver-stage.finish'}
            or name.startswith('tools/webview-profile/')
            or name.startswith('app/rescue-host-shell.exe.WebView2/'))


def verify(folder, expected, after_native=False):
    value = read_manifest((folder / 'BUILD.json').read_bytes(), expected)
    expected_names = {row['path'] for row in value['files']} | {'BUILD.json'}
    actual = set()
    for path in folder.rglob('*'):
        if path.is_symlink():
            raise ValueError('REPRO_SYMLINK_REJECTED')
        if path.is_file():
            actual.add(path.relative_to(folder).as_posix())
    extras = actual - expected_names
    if not expected_names.issubset(actual) or (extras and not (
            after_native and all(runtime_file(name) for name in extras))):
        raise ValueError('REPRO_FILE_SET_REJECTED')
    for row in value['files']:
        path = folder / row['path']
        if path.stat().st_size != row['bytes'] or digest(path.read_bytes()) != row['sha256']:
            raise ValueError('REPRO_BINARY_OR_SOURCE_DRIFT')
    return value


def pack(app, tools, license_path, workflow, output):
    if output.exists():
        raise ValueError('REPRO_OUTPUT_EXISTS')
    files = {BINARY_PATHS[0]: app, BINARY_PATHS[1]: tools / 'tauri/bin/tauri-driver.exe',
             'tools/driver-provenance.json': tools / 'driver-provenance.json',
             'tools/tauri-driver.crate': tools / 'tauri-driver.crate',
             'SOURCE-LICENSE.txt': license_path}
    for directory in ('edge', 'tauri-driver-2.1.0'):
        for path in (tools / directory).rglob('*'):
            if path.is_symlink():
                raise ValueError('REPRO_SYMLINK_REJECTED')
            if path.is_file():
                files['tools/' + path.relative_to(tools).as_posix()] = path
    rows = []
    total = 0
    for name, path in sorted(files.items()):
        safe_name(name)
        if path.is_symlink() or not allowed(name):
            raise ValueError('REPRO_INPUT_REJECTED')
        size = path.stat().st_size
        total += size
        if total > ZIP_LIMIT:
            raise ValueError('REPRO_PAYLOAD_TOO_LARGE')
        rows.append({'path': name, 'bytes': size, 'sha256': digest(path.read_bytes())})
    manifest = {'schema': 1, 'purpose': 'pr180-finite-reproduction', 'sourceCommit': SOURCE,
                'workflowCommit': workflow, 'profile': 'debug', 'features': ['custom-protocol'],
                'distribution': False, 'files': rows, 'payloadBytes': total}
    raw = (json.dumps(manifest, sort_keys=True, separators=(',', ':')) + '\n').encode()
    expected = digest(raw)
    read_manifest(raw, expected)
    output.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(output, 'x', compression=zipfile.ZIP_STORED) as archive:
        archive.writestr('BUILD.json', raw)
        for name, path in sorted(files.items()):
            archive.write(path, name)
    if output.stat().st_size > ZIP_LIMIT:
        output.unlink()
        raise ValueError('REPRO_ARCHIVE_TOO_LARGE')
    return expected, output.stat().st_size


def unpack(archive_path, folder, expected):
    if folder.exists() or archive_path.stat().st_size > ZIP_LIMIT:
        raise ValueError('REPRO_UNPACK_BOUND_REJECTED')
    with zipfile.ZipFile(archive_path) as archive:
        entries = archive.infolist()
        if len(entries) > 257 or len({e.filename for e in entries}) != len(entries):
            raise ValueError('REPRO_ARCHIVE_FILE_SET_REJECTED')
        for entry in entries:
            safe_name(entry.filename)
            if (entry.is_dir() or stat.S_ISLNK(entry.external_attr >> 16)
                    or entry.file_size > LIMIT):
                raise ValueError('REPRO_ARCHIVE_ENTRY_REJECTED')
        manifest_entry = archive.getinfo('BUILD.json')
        if manifest_entry.file_size > 65536:
            raise ValueError('REPRO_MANIFEST_TOO_LARGE')
        value = read_manifest(archive.read(manifest_entry), expected)
        if {e.filename for e in entries} != {r['path'] for r in value['files']} | {'BUILD.json'}:
            raise ValueError('REPRO_ARCHIVE_FILE_SET_REJECTED')
        sizes = {r['path']: r['bytes'] for r in value['files']}
        if any(e.filename != 'BUILD.json' and e.file_size != sizes[e.filename] for e in entries):
            raise ValueError('REPRO_ARCHIVE_SIZE_REJECTED')
        folder.mkdir()
        for entry in entries:
            dest = folder / entry.filename
            dest.parent.mkdir(parents=True, exist_ok=True)
            with archive.open(entry) as source, dest.open('xb') as target:
                shutil.copyfileobj(source, target, 65536)
    return verify(folder, expected)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('mode', choices=['pack', 'unpack', 'verify'])
    for name in ('app', 'tools', 'license', 'archive', 'directory'):
        parser.add_argument('--' + name, type=Path)
    parser.add_argument('--workflow')
    parser.add_argument('--manifest-sha256')
    parser.add_argument('--after-native', action='store_true')
    args = parser.parse_args()
    try:
        if args.mode == 'pack':
            expected, size = pack(args.app, args.tools, args.license, args.workflow, args.archive)
            if os.environ.get('GITHUB_OUTPUT'):
                with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
                    output.write('manifestSha256=' + expected + '\nbundleBytes=' + str(size) + '\n')
            print('REPRO_BUNDLE_BOUNDED', size, expected)
        elif args.mode == 'unpack':
            unpack(args.archive, args.directory, args.manifest_sha256)
            print('REPRO_BUNDLE_VERIFIED')
        else:
            verify(args.directory, args.manifest_sha256, args.after_native)
            print('REPRO_BUNDLE_UNCHANGED')
        return 0
    except (OSError, ValueError, KeyError, TypeError, AttributeError, zipfile.BadZipFile):
        print('REPRO_BUNDLE_REJECTED')
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
