"""Reconcile recorded producer modules with fixed Go texts; never grant distribution."""
import sys

# Keep the read-only CLI from writing imported-helper bytecode beside source.
sys.dont_write_bytecode = True

import argparse
from datetime import datetime
import json
import os
from pathlib import Path
import re

import build_mac_sealed_producer as builder
import collect_go_licenses as licenses
import package_mac_agent as mac
from go_ci import product_files_for
from verify_mac_evaluation_candidate import exact_tree, CandidateError

RECORD_LIMIT = 256 * 1024
GAPS = ['source-and-recipe-correspondence', 'recorded-module-closure-completeness',
        'linked-binary-dependencies', 'stdlib-vendored-notices', 'platform-runtime-notices',
        'build-tool-dependencies', 'toolchain-sdk-provenance', 'legal-notice-sufficiency',
        'binary-secret-path-audit', 'publisher-and-signature', 'notarization',
        'enrollment-and-verify-to-exec', 'runtime-and-cleanup']


class NoticeError(Exception):
    pass


def require(condition, code):
    if not condition:
        raise NoticeError(code)


def same(actual, expected):
    # Reject 0/1 in place of booleans throughout nested records.
    return json.dumps(actual, sort_keys=True) == json.dumps(expected, sort_keys=True)


def hex_value(value, length):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{' + str(length) + '}', value) is not None


def strict_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'GO_NOTICE_DUPLICATE_FIELD')
        result[key] = value
    return result


def reject_constant(value):
    raise NoticeError('GO_NOTICE_JSON_INVALID')


def parse(data):
    return json.loads(data.decode('utf-8'), object_pairs_hook=strict_object,
                      parse_constant=reject_constant)


def hash_map(value, paths):
    return (isinstance(value, dict) and set(value) == set(paths)
            and all(hex_value(digest, 64) for digest in value.values()))


def check_record(record, source, locks, image, image_sha):
    fixed = {
        'schema': 'tether-mac-sealed-producer-build/1', 'repository': 'O2-CONNECTIVE/Tether',
        'status': 'blocked', 'activation': 'blocked', 'distribution': 'blocked',
        'sourceCommit': source, 'target': 'darwin-arm64', 'role': builder.ROLE,
        'flags': list(builder.FLAGS),
        'settings': {'GOVERSION': licenses.GO_VERSION, 'GOOS': 'darwin', 'GOARCH': 'arm64',
                     'CGO_ENABLED': '1'},
        'producerExecutable': True, 'producerExecuted': False, 'signingPerformed': False,
        'enrollmentAccessed': False, 'cryptographicAttestation': False,
        'blockers': list(builder.BLOCKERS),
    }
    variable = {'sourceTree', 'buildUTC', 'snapshotGoFiles', 'locks', 'recipe',
                'compiledRepositoryPackages', 'modules', 'image'}
    require(isinstance(record, dict) and set(record) == set(fixed) | variable,
            'GO_NOTICE_BUILD_SCHEMA_INVALID')
    require(record['sourceCommit'] == source, 'GO_NOTICE_SOURCE_MISMATCH')
    require(all(same(record[key], value) for key, value in fixed.items()),
            'GO_NOTICE_BUILD_BOUNDARY_INVALID')
    require(hex_value(record['sourceTree'], 40), 'GO_NOTICE_BUILD_SCHEMA_INVALID')
    stamp = record['buildUTC']
    require(isinstance(stamp, str) and re.fullmatch(r'[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z', stamp),
            'GO_NOTICE_BUILD_SCHEMA_INVALID')
    datetime.strptime(stamp, '%Y-%m-%dT%H:%M:%SZ')
    require(same(record['locks'], {'backend/' + name: mac.sha(data) for name, data in locks.items()}),
            'GO_NOTICE_LOCK_MISMATCH')
    packages = product_files_for('darwin', True)
    files = {'backend/' + path[len(builder.source.PROJECT):].lstrip('/') + '/' + name
             for path, names in packages.items() for name in names}
    require(hash_map(record['snapshotGoFiles'], files) and hash_map(record['recipe'], builder.RECIPE),
            'GO_NOTICE_BUILD_SCHEMA_INVALID')
    selected = record['compiledRepositoryPackages']
    require(isinstance(selected, list) and all(isinstance(path, str) for path in selected)
            and len(selected) == len(set(selected)) and set(selected) <= set(packages)
            and builder.ROLE in selected, 'GO_NOTICE_BUILD_SCHEMA_INVALID')
    expected_image = {'path': 'resident-pipe', 'sha256': image_sha, 'bytes': len(image),
                      'kind': 'mach-o-executable', 'header': mac.macho(image, 'darwin-arm64')}
    require(same(record['image'], expected_image), 'GO_NOTICE_IMAGE_RECORD_MISMATCH')
    modules = record['modules']
    require(isinstance(modules, list) and len(modules) <= 128, 'GO_NOTICE_MODULE_INVALID')
    seen = set()
    for item in modules:
        require(isinstance(item, dict) and set(item) == {'Path', 'Version', 'Sum', 'GoModSum'}
                and all(isinstance(value, str) for value in item.values()), 'GO_NOTICE_MODULE_INVALID')
        name = item['Path']
        require(name in licenses.MODULES, 'GO_NOTICE_MODULE_UNCOVERED')
        require(name not in seen, 'GO_NOTICE_MODULE_INVALID')
        seen.add(name)
        require((item['Version'], item['Sum'], item['GoModSum']) == licenses.MODULES[name],
                'GO_NOTICE_MODULE_IDENTITY_MISMATCH')
    return seen


def verify(producer_root, notices_root, module_root, source, record_sha, image_sha):
    require(hex_value(source, 40) and hex_value(record_sha, 64) and hex_value(image_sha, 64),
            'GO_NOTICE_EXPECTATION_INVALID')
    producer_root, notices_root, module_root = [Path(os.path.abspath(path))
                                               for path in (producer_root, notices_root, module_root)]
    # Only producer/notice outputs are exact trees. module_root is a source
    # directory: read its two fixed lock files, never enumerate its other files.
    producer_files = {'BUILD_RECORD.json', 'resident-pipe'}
    notice_files = set(mac.TEXTS) | {'DEPENDENCIES.json'}
    exact_tree(producer_root, producer_files)
    exact_tree(notices_root, notice_files)
    locks = {name: mac.read_regular(module_root / name, mac.TEXT_LIMIT) for name in ('go.mod', 'go.sum')}
    licenses.check_locks(locks)
    raw_record = mac.read_regular(producer_root / 'BUILD_RECORD.json', RECORD_LIMIT)
    require(mac.sha(raw_record) == record_sha, 'GO_NOTICE_BUILD_HASH_MISMATCH')
    image = mac.read_regular(producer_root / 'resident-pipe', mac.BINARY_LIMIT)
    require(mac.sha(image) == image_sha, 'GO_NOTICE_IMAGE_HASH_MISMATCH')
    selected = check_record(parse(raw_record), source, locks, image, image_sha)
    inventory_bytes = mac.read_regular(notices_root / 'DEPENDENCIES.json', 64 * 1024)
    inventory = parse(inventory_bytes)
    require(same(inventory, licenses.inventory_record(source, locks)), 'GO_NOTICE_INVENTORY_MISMATCH')
    for path, expected in mac.TEXTS.items():
        require(mac.sha(mac.read_regular(notices_root / path, mac.TEXT_LIMIT)) == expected,
                'GO_NOTICE_TEXT_HASH_MISMATCH')
    # Shape and record rereads are not an atomic snapshot or verify-to-exec protection.
    require(mac.read_regular(producer_root / 'BUILD_RECORD.json', RECORD_LIMIT) == raw_record
            and mac.read_regular(notices_root / 'DEPENDENCIES.json', 64 * 1024) == inventory_bytes,
            'GO_NOTICE_RECORD_CHANGED')
    exact_tree(producer_root, producer_files)
    exact_tree(notices_root, notice_files)
    return {'check': 'go-notice-closure', 'status': 'blocked', 'selectedModuleNoticesMatched': True,
            'scope': 'recorded-selected-modules-and-fixed-texts-only',
            'sourceCommitDeclared': source, 'buildRecordSha256': record_sha,
            'producerSha256': image_sha, 'inventorySha256': mac.sha(inventory_bytes),
            'selectedModuleCount': len(selected), 'fixedTextCount': len(mac.TEXTS),
            'fixedModulesNotSelected': sorted(set(licenses.MODULES) - selected),
            'binaryDependenciesVerified': False, 'noticeCompletenessVerified': False,
            'distributionStatus': 'blocked', 'productionGateClosed': True,
            'unverifiedScopes': GAPS.copy()}


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise NoticeError('GO_NOTICE_ARGUMENT_INVALID')


def main(argv=None):
    try:
        parser = Parser(description=__doc__, allow_abbrev=False)
        for name in ('producer-root', 'notices-root', 'module-root', 'source',
                     'build-record-sha256', 'producer-sha256'):
            parser.add_argument('--' + name, required=True)
        args = parser.parse_args(argv)
        result = verify(args.producer_root, args.notices_root, args.module_root, args.source,
                        args.build_record_sha256, args.producer_sha256)
        code = 2  # A match never grants notice, signing, activation or distribution approval.
    except NoticeError as error:
        result, code = {'check': 'go-notice-closure', 'status': 'refused', 'code': str(error)}, 1
    except (mac.PackageError, licenses.CollectionError, CandidateError):
        result, code = {'check': 'go-notice-closure', 'status': 'refused', 'code': 'GO_NOTICE_MATERIAL_INVALID'}, 1
    except (OSError, ValueError, TypeError, KeyError, AttributeError, RecursionError, OverflowError):
        result, code = {'check': 'go-notice-closure', 'status': 'refused', 'code': 'GO_NOTICE_INPUT_INVALID'}, 1
    print(json.dumps(result, sort_keys=True))
    return code


if __name__ == '__main__':
    raise SystemExit(main())
