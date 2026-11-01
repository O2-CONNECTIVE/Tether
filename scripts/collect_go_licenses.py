"""固定Goと3moduleの本文を収集する。Go実行・取得・配布判定は行わない。"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat


GO_VERSION = 'go1.27.3'
MODULES = {
    'github.com/coder/websocket': ('v1.8.15',
                                 'h1:6B2JPeOGlpff2Uz6vOEH1Vzpi0iUz20A+lPVhPHtNUA=',
                                 'h1:NX3SzP+inril6yawo5CQXx8+fk145lPDC6pumgx0mVg='),
    'golang.org/x/crypto': ('v0.58.0',
                          'h1:COkYLr4k7nDI4r6QtMML2AfzeIswWHAJthLS6K/M/54=',
                          'h1:Xh+kl5A+M0gMiWDytH2Hhr5Z7WbNCpv/eB/EFgfSk+w='),
    'golang.org/x/sys': ('v0.49.0',
                       'h1:XbzkgYJHdqh/8m2Uu0W/dQv8nktxx4BFHp1M0gROTXA=',
                       'h1:hNLxWAXmnKAxqDtdwIYC4bM9oQPEecfsnNMuSxOs3og='),
}
TEXT_SHA = {'LICENSE': '911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad',
            'PATENTS': '96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc'}
# Fixed-tag upstream text digests; module h1 and toolchain provenance are separate.
WEBSOCKET_TEXT_SHA = {
    'LICENSE.txt': 'cc0975a5f6305145bdd7b41ce9479632fdac3870e6ac4281f28017f18c767c4e',
}
MODULE_TEXTS = {
    'github.com/coder/websocket': ('https://github.com/coder/websocket', WEBSOCKET_TEXT_SHA),
    'golang.org/x/crypto': ('https://github.com/golang/crypto', TEXT_SHA),
    'golang.org/x/sys': ('https://github.com/golang/sys', TEXT_SHA),
}


class CollectionError(Exception):
    """原入力を含めない固定codeのみ。"""


class SafeParser(argparse.ArgumentParser):
    def error(self, message):
        raise CollectionError('GO_LICENSE_ARGUMENT_INVALID')


def digest(data):
    return hashlib.sha256(data).hexdigest()


def require(condition, code):
    if not condition:
        raise CollectionError(code)


def ordinary(path, directory=False):
    observed = path.lstat()
    kind = stat.S_ISDIR if directory else stat.S_ISREG
    require(kind(observed.st_mode) and not getattr(observed, 'st_file_attributes', 0)
            & getattr(stat, 'FILE_ATTRIBUTE_REPARSE_POINT', 0), 'GO_LICENSE_FILE_INVALID')
    return observed


def read_file(path, limit):
    before = ordinary(path)
    require(before.st_size <= limit, 'GO_LICENSE_SIZE_LIMIT')
    flags = os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0) | getattr(os, 'O_NONBLOCK', 0)
    with os.fdopen(os.open(path, flags), 'rb') as stream:
        opened = os.fstat(stream.fileno())
        require(stat.S_ISREG(opened.st_mode) and
                (before.st_dev, before.st_ino) == (opened.st_dev, opened.st_ino),
                'GO_LICENSE_FILE_INVALID')
        data = stream.read(limit + 1)
    require(len(data) <= limit, 'GO_LICENSE_SIZE_LIMIT')
    return data


def fixed_directory(root, relative=''):
    ordinary(root, directory=True)
    current = root
    for part in Path(relative).parts:
        current /= part
        ordinary(current, directory=True)
    return current


def strict_object(pairs):
    result = {}
    for name, value in pairs:
        require(name not in result, 'GO_LICENSE_METADATA_DUPLICATE')
        result[name] = value
    return result


def check_metadata(data):
    decoder = json.JSONDecoder(object_pairs_hook=strict_object)
    remaining, modules = data.decode('utf-8').strip(), {}
    required = {'Path', 'Version', 'Sum', 'GoModSum'}
    optional = {'Time', 'Indirect', 'Dir', 'GoMod', 'GoVersion'}
    while remaining:
        value, end = decoder.raw_decode(remaining)
        require(isinstance(value, dict) and value.get('Path') in MODULES,
                'GO_LICENSE_MODULE_SET_INVALID')
        require(required <= value.keys() and value.keys() <= required | optional,
                'GO_LICENSE_METADATA_INVALID')
        name = value['Path']
        require(name not in modules, 'GO_LICENSE_MODULE_SET_INVALID')
        require((value['Version'], value['Sum'], value['GoModSum']) == MODULES[name],
                'GO_LICENSE_MODULE_IDENTITY_MISMATCH')
        # Dir/GoMod are deliberately not used as filesystem selectors or output.
        require(all(isinstance(value[key], str) for key in value.keys() - {'Indirect'}),
                'GO_LICENSE_METADATA_INVALID')
        require('Indirect' not in value or type(value['Indirect']) is bool,
                'GO_LICENSE_METADATA_INVALID')
        modules[name] = value
        remaining = remaining[end:].strip()
    require(set(modules) == set(MODULES), 'GO_LICENSE_MODULE_SET_INVALID')


def check_locks(locks):
    # Deliberately narrow grammar: exact module/go declarations and fixed
    # require entries, either individual or a block. No comments, replacement,
    # toolchain/exclude directive, quoted path or extra dependency is accepted.
    tokens = locks['go.mod'].decode('utf-8').split()
    require(tokens[:4] == ['module', 'github.com/O2-CONNECTIVE/rescue-host/backend',
                           'go', '1.27.3'], 'GO_LICENSE_LOCK_MISMATCH')
    remaining, found = tokens[4:], {}
    while remaining:
        require(len(remaining) >= 3 and remaining[0] == 'require', 'GO_LICENSE_LOCK_MISMATCH')
        remaining = remaining[1:]
        grouped = remaining[0] == '('
        if grouped:
            remaining = remaining[1:]
        count = 0
        while remaining and (not grouped or remaining[0] != ')'):
            require(len(remaining) >= 2, 'GO_LICENSE_LOCK_MISMATCH')
            name, version = remaining[:2]
            require(name in MODULES and name not in found and version == MODULES[name][0],
                    'GO_LICENSE_LOCK_MISMATCH')
            found[name] = version
            count += 1
            remaining = remaining[2:]
            if not grouped:
                break
        if grouped:
            require(count > 0 and remaining and remaining[0] == ')', 'GO_LICENSE_LOCK_MISMATCH')
            remaining = remaining[1:]
    require(set(found) == set(MODULES), 'GO_LICENSE_LOCK_MISMATCH')
    expected_sum = []
    for name, (version, module_sum, mod_sum) in MODULES.items():
        expected_sum += [[name, version, module_sum], [name, version + '/go.mod', mod_sum]]
    actual = [line.split() for line in locks['go.sum'].decode('utf-8').splitlines() if line.strip()]
    require(sorted(actual) == sorted(expected_sum), 'GO_LICENSE_LOCK_MISMATCH')


def collect(args):
    require(re.fullmatch('[0-9a-f]{40}', args.source) is not None, 'GO_LICENSE_SOURCE_INVALID')
    fixed_directory(args.module_root)
    fixed_directory(args.go_root)
    fixed_directory(args.module_cache)
    fixed_directory(args.metadata.parent)
    fixed_directory(args.output.parent)
    require(not args.output.exists() and not args.output.is_symlink(), 'GO_LICENSE_OUTPUT_EXISTS')
    locks = {name: read_file(args.module_root / name, 16 * 1024) for name in ('go.mod', 'go.sum')}
    check_locks(locks)
    check_metadata(read_file(args.metadata, 64 * 1024))
    version = read_file(args.go_root / 'VERSION', 512).decode('utf-8')
    require(re.fullmatch(r'go1\.27\.3\n(?:time [0-9T:Z-]+\n)?', version) is not None,
            'GO_LICENSE_TOOLCHAIN_VERSION_MISMATCH')
    files = {}

    def texts(root, destination, hashes):
        records = {}
        for name in hashes:
            data = read_file(root / name, 16 * 1024)
            require(digest(data) == hashes[name], 'GO_LICENSE_TEXT_HASH_MISMATCH')
            path = destination + '/' + name
            files[path] = data
            records[name] = {'path': path, 'sha256': digest(data)}
        return records

    inventory = {
        'schema': 'rescue-host-go-license-inventory/1', 'sourceCommit': args.source,
        'sourceCommitVerified': False,
        'scope': 'fixed-direct-modules-and-toolchain-texts-only', 'distributionStatus': 'blocked',
        'moduleGraphVerified': False, 'binaryDependenciesVerified': False,
        'productionGateClosed': True,
        'lockFiles': {name: digest(data) for name, data in locks.items()},
        'toolchain': {'version': GO_VERSION, 'source': 'https://github.com/golang/go/tree/' + GO_VERSION,
                      'archiveVerified': False,
                      'texts': texts(args.go_root, 'go-toolchain/' + GO_VERSION, TEXT_SHA)},
        'modules': [],
    }
    for module, (version, module_sum, mod_sum) in MODULES.items():
        name = module.rsplit('/', 1)[1]
        upstream, hashes = MODULE_TEXTS[module]
        inventory['modules'].append({
            'module': module, 'version': version, 'moduleSum': module_sum, 'goModSum': mod_sum,
            'source': upstream + '/tree/' + version,
            'directoryVerified': False,
            'texts': texts(fixed_directory(args.module_cache, module + '@' + version),
                           name + '-' + version, hashes),
        })
    files['DEPENDENCIES.json'] = (json.dumps(inventory, indent=2, sort_keys=True) + '\n').encode()
    try:
        args.output.mkdir(exist_ok=False)
        for name, data in files.items():
            destination = args.output / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            with destination.open('xb') as stream:
                stream.write(data)
    except OSError as error:
        # Leave a partial output for inspection; never delete or overwrite it on retry.
        raise CollectionError('GO_LICENSE_OUTPUT_INCOMPLETE') from error


def main():
    parser = SafeParser(description=__doc__, allow_abbrev=False)
    for name in ('module-root', 'module-cache', 'go-root', 'metadata', 'output'):
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--source', required=True)
    try:
        collect(parser.parse_args())
        report = {'check': 'go-license-inventory', 'status': 'collected', 'textCount': 7,
                  'distributionStatus': 'blocked', 'productionGateClosed': True}
        code = 0
    except CollectionError as error:
        report = {'check': 'go-license-inventory', 'status': 'fail', 'code': str(error)}
        code = 1
    except (OSError, ValueError, TypeError, KeyError, AttributeError, RecursionError):
        report = {'check': 'go-license-inventory', 'status': 'fail', 'code': 'GO_LICENSE_INVALID'}
        code = 1
    print(json.dumps(report, sort_keys=True))
    return code


if __name__ == '__main__':
    raise SystemExit(main())
