"""Read a blocked Mac candidate's exact materials; never launch, package or publish."""
import argparse
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import plistlib
import re
import stat

try:
    from scripts import package_mac_agent as mac
except ModuleNotFoundError:
    import package_mac_agent as mac

SCHEMA = 'tether-mac-evaluation-materials/1'
LABEL = 'カレントの検証版'
IMAGES = {'gui': 'Contents/MacOS/rescue-host-shell',
          'producer': 'Contents/Helpers/resident-pipe'}
LOCKS = {'locks/go.mod', 'locks/go.sum', 'locks/Cargo.lock', 'locks/package-lock.json'}
FIXED = {'LICENSE', 'RECIPIENT.txt'} | LOCKS
GO_NOTICES = {'notices/go/' + name: digest for name, digest in mac.TEXTS.items()}
GUI_NOTICES = {'notices/gui/FRONTEND_THIRD_PARTY_NOTICES.txt',
               'notices/gui/rust-toolchain/COPYRIGHT.html',
               'notices/gui/rust-toolchain/COPYRIGHT-library.html'}
GAPS = ['binary-source-correspondence', 'linked-dependency-closure',
        'stdlib-vendored-notice-completeness', 'platform-runtime-notices',
        'build-tool-dependencies', 'toolchain-sdk-provenance',
        'binary-secret-audit', 'publisher-and-signature', 'notarization',
        'verify-to-exec', 'runtime-and-cleanup']
FLAGS = {'binarySourceCorrespondenceVerified', 'noticeCompletenessVerified',
         'binarySecretAuditVerified', 'publisherVerified', 'notarizationVerified',
         'runtimeVerified'}
TEXT_LIMIT = 4 * 1024 * 1024
IMAGE_LIMIT = 256 * 1024 * 1024
TOTAL_LIMIT = 640 * 1024 * 1024
MANIFEST_LIMIT = 128 * 1024
FILE_LIMIT = 2048


class CandidateError(Exception):
    pass


def require(condition, code):
    if not condition:
        raise CandidateError(code)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def hex_value(value, size):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{' + str(size) + '}', value) is not None


def strict_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'CANDIDATE_DUPLICATE_FIELD')
        result[key] = value
    return result


def safe_path(value):
    if not isinstance(value, str) or len(value) > 240:
        return False
    return all(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.+-]*', part) and
               not part.endswith('.') and not re.fullmatch(
                   r'(?i)(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\..*)?', part)
               for part in value.split('/'))


def manifest_record(data, source, hashes):
    record = json.loads(data, object_pairs_hook=strict_object)
    fields = {'schema', 'repository', 'sourceCommit', 'buildUTC', 'target', 'label',
              'appDirectory', 'distributionStatus', 'signingStatus',
              'productionGateClosed', 'unverifiedScopes', 'images', 'files', 'notices'} | FLAGS
    require(isinstance(record, dict) and set(record) == fields, 'CANDIDATE_SCHEMA_INVALID')
    require(record['schema'] == SCHEMA and record['repository'] == 'O2-CONNECTIVE/Tether'
            and record['label'] == LABEL and record['target'] in mac.TARGETS,
            'CANDIDATE_SCHEMA_INVALID')
    require(record['sourceCommit'] == source, 'CANDIDATE_SOURCE_MISMATCH')
    stamp = record['buildUTC']
    require(isinstance(stamp, str) and re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z', stamp),
            'CANDIDATE_TIME_INVALID')
    try:
        datetime.strptime(stamp, '%Y-%m-%dT%H:%M:%SZ')
    except ValueError:
        raise CandidateError('CANDIDATE_TIME_INVALID') from None
    require(record['distributionStatus'] == 'blocked' and
            record['signingStatus'] == 'unverified' and record['productionGateClosed'] is True
            and all(record[field] is False for field in FLAGS)
            and record['unverifiedScopes'] == GAPS, 'CANDIDATE_BOUNDARY_INVALID')
    app = record['appDirectory']
    require(safe_path(app) and '/' not in app and app.endswith('.app')
            and app != 'TetherCurrentGUIFixture.app', 'CANDIDATE_APP_INVALID')
    images = {role: app + '/' + path for role, path in IMAGES.items()}
    require(isinstance(record['images'], dict) and set(record['images']) == set(IMAGES),
            'CANDIDATE_IMAGE_SET_INVALID')
    for role, path in images.items():
        require(record['images'][role] == {'path': path, 'sha256': hashes[role]},
                'CANDIDATE_IMAGE_MISMATCH')
    files, notices = record['files'], record['notices']
    require(isinstance(files, dict) and len(files) <= FILE_LIMIT and
            all(safe_path(path) and hex_value(value, 64) for path, value in files.items()),
            'CANDIDATE_FILE_SET_INVALID')
    require(isinstance(notices, list) and len(notices) <= FILE_LIMIT and
            all(safe_path(path) and path.startswith('notices/') for path in notices)
            and len(notices) == len(set(notices)), 'CANDIDATE_NOTICE_SET_INVALID')
    require(set(GO_NOTICES) | GUI_NOTICES <= set(notices), 'CANDIDATE_NOTICE_MISSING')
    for path in set(notices) - set(GO_NOTICES) - GUI_NOTICES:
        require(path.startswith('notices/gui/rust/') or path.startswith('notices/go/stdlib-vendor/'),
                'CANDIDATE_NOTICE_SET_INVALID')
        require(PurePosixPath(path).name.lower().split('.')[0] in
                {'license', 'licence', 'copying', 'notice', 'notices', 'copyright'},
                'CANDIDATE_NOTICE_SET_INVALID')
    require(any(path.startswith('notices/gui/rust/') for path in notices),
            'CANDIDATE_NOTICE_MISSING')
    required = FIXED | set(images.values()) | {app + '/Contents/Info.plist'} | set(notices)
    require(set(files) == required and all(files[path] == hashes[role] for role, path in images.items()),
            'CANDIDATE_FILE_SET_INVALID')
    require(all(files[path] == expected for path, expected in GO_NOTICES.items()),
            'CANDIDATE_FIXED_NOTICE_MISMATCH')
    return record, images


def exact_tree(root, expected):
    """Bound traversal and reject links/special files without opening their targets."""
    directories = {str(parent) for name in expected for parent in PurePosixPath(name).parents
                   if str(parent) != '.'}
    pending, found, seen_directories = [root], set(), set()
    count = 0
    while pending:
        directory = pending.pop()
        mac.directory_chain(directory)
        with os.scandir(directory) as entries:
            for entry in entries:
                count += 1
                require(count <= FILE_LIMIT * 8, 'CANDIDATE_SIZE_LIMIT')
                path = Path(entry.path).relative_to(root).as_posix()
                require(safe_path(path), 'CANDIDATE_MEMBER_INVALID')
                info = entry.stat(follow_symlinks=False)
                require(not getattr(info, 'st_file_attributes', 0) & 1024, 'CANDIDATE_MEMBER_INVALID')
                if stat.S_ISDIR(info.st_mode):
                    require(path in directories, 'CANDIDATE_MEMBER_SET_MISMATCH')
                    seen_directories.add(path)
                    pending.append(Path(entry.path))
                else:
                    require(stat.S_ISREG(info.st_mode), 'CANDIDATE_MEMBER_INVALID')
                    require(path in expected, 'CANDIDATE_MEMBER_SET_MISMATCH')
                    found.add(path)
    require(found == expected and seen_directories == directories, 'CANDIDATE_MEMBER_SET_MISMATCH')
    require(len({name.casefold() for name in found | seen_directories}) == len(found | seen_directories),
            'CANDIDATE_MEMBER_AMBIGUOUS')


def verify(root, source, manifest_sha256, hashes):
    require(hex_value(source, 40) and hex_value(manifest_sha256, 64) and
            set(hashes) == set(IMAGES) and all(hex_value(value, 64) for value in hashes.values()),
            'CANDIDATE_EXPECTATION_INVALID')
    root = Path(os.path.abspath(root))
    mac.directory_chain(root)
    data = mac.read_regular(root / 'MANIFEST.json', MANIFEST_LIMIT)
    require(digest(data) == manifest_sha256, 'CANDIDATE_MANIFEST_HASH_MISMATCH')
    record, images = manifest_record(data, source, hashes)
    expected = set(record['files']) | {'MANIFEST.json'}
    exact_tree(root, expected)
    total, declarations = len(data), {}
    for path, wanted in sorted(record['files'].items()):
        value = mac.read_regular(root / path, IMAGE_LIMIT if path in images.values() else TEXT_LIMIT)
        total += len(value)
        require(total <= TOTAL_LIMIT, 'CANDIDATE_SIZE_LIMIT')
        require(digest(value) == wanted, 'CANDIDATE_FILE_HASH_MISMATCH')
        if path in images.values():
            role = next(role for role, name in images.items() if name == path)
            declarations[role] = mac.macho(value, record['target'])
        elif path.endswith('/Contents/Info.plist'):
            plist = plistlib.loads(value)
            require(isinstance(plist, dict) and plist.get('CFBundleExecutable') == 'rescue-host-shell'
                    and plist.get('CFBundleDisplayName') == LABEL and plist.get('CFBundlePackageType') == 'APPL',
                    'CANDIDATE_PLIST_MISMATCH')
        else:
            require(bool(value.decode('utf-8').strip()), 'CANDIDATE_TEXT_INVALID')
    # Catch completed metadata/shape changes. This is not an adversarial filesystem
    # snapshot or verify-to-exec guarantee, and nothing is run after these reads.
    require(mac.read_regular(root / 'MANIFEST.json', MANIFEST_LIMIT) == data,
            'CANDIDATE_MANIFEST_CHANGED')
    exact_tree(root, expected)
    return {'check': 'mac-evaluation-materials', 'status': 'blocked', 'materialsMatched': True,
            'distributionStatus': 'blocked', 'productionGateClosed': True,
            'sourceCommitDeclared': source, 'manifestSha256': manifest_sha256,
            'imageHashes': hashes, 'platformDeclarations': declarations,
            'fileCount': len(record['files']), 'unverifiedScopes': GAPS.copy()}


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise CandidateError('CANDIDATE_ARGUMENT_INVALID')


def main(argv=None):
    try:
        parser = Parser(description=__doc__, allow_abbrev=False)
        for name in ('root', 'source', 'manifest-sha256', 'gui-sha256', 'producer-sha256'):
            parser.add_argument('--' + name, required=True)
        args = parser.parse_args(argv)
        result = verify(args.root, args.source, args.manifest_sha256,
                        {'gui': args.gui_sha256, 'producer': args.producer_sha256})
        code = 2  # Material consistency never returns release/launch approval.
    except (CandidateError, mac.PackageError) as error:
        result, code = {'check': 'mac-evaluation-materials', 'status': 'refused', 'code': str(error)}, 1
    except (OSError, ValueError, TypeError, KeyError, AttributeError, RecursionError, OverflowError):
        result, code = {'check': 'mac-evaluation-materials', 'status': 'refused',
                        'code': 'CANDIDATE_INPUT_INVALID'}, 1
    print(json.dumps(result, ensure_ascii=True, sort_keys=True))
    return code


if __name__ == '__main__':
    raise SystemExit(main())
