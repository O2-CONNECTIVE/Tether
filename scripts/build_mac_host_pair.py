"""Build the fixed GUI pairing helper as a recorded, nonexecuted Mac image."""
from datetime import datetime, timezone
import ipaddress
import json
import os
from pathlib import Path
from pathlib import PurePosixPath
import platform
import re
import shutil
import tempfile
from urllib.parse import urlsplit

import build_mac_resident_source as source
import collect_go_licenses as licenses
import package_mac_agent as image
from go_ci import product_files_for

ROLE = source.PROJECT + '/cmd/tether-host-pair'
FLAGS = ('-mod=readonly', '-trimpath', '-buildvcs=false', '-buildmode=exe', '-p=1')
RECIPE = ('scripts/build_mac_host_pair.py', 'scripts/build_mac_resident_source.py',
          'scripts/go_ci.py', 'scripts/package_mac_agent.py', 'scripts/collect_go_licenses.py')
CONFIG_KEYS = {'gateway': 'TETHER_PAIR_GATEWAY', 'issuer': 'TETHER_PAIR_ISSUER',
               'clientID': 'TETHER_PAIR_CLIENT_ID'}
BUILD_VARS = {'gateway': 'guiGateway', 'issuer': 'guiIssuer', 'clientID': 'guiClientID'}
LIMIT = 256 * 1024 * 1024
BLOCKERS = ('GUI_BUILD_UNBOUND', 'RELEASE_IDENTITY_UNCONFIGURED', 'SIGNING_UNVERIFIED',
            'ENROLLMENT_UNCONFIGURED', 'PLACEMENT_UNVERIFIED', 'VERIFY_TO_EXEC_UNVERIFIED',
            'TOOLCHAIN_SDK_INTEGRITY_UNVERIFIED', 'RUNTIME_COMPATIBILITY_UNVERIFIED',
            'NOTICE_CLOSURE_UNAUDITED', 'BINARY_SECRET_PATH_AUDIT_PENDING')


class PairBuildError(Exception):
    pass


def require(condition, code):
    if not condition:
        raise PairBuildError(code)


def ascii_visible(value):
    return isinstance(value, str) and all(0x21 <= ord(char) <= 0x7e for char in value)


def valid_gateway(value):
    if not isinstance(value, str) or not value:
        return False
    try:
        parsed = urlsplit(value)
        host = parsed.hostname
        port = parsed.port
    except ValueError:
        return False
    if (parsed.scheme != 'https' or not parsed.netloc or parsed.username is not None or
            parsed.password is not None or parsed.path or parsed.query or parsed.fragment or
            '?' in value or '#' in value or value != 'https://' + parsed.netloc or
            parsed.netloc != parsed.netloc.lower() or not host):
        return False
    if port is not None and not 1 <= port <= 65535:
        return False
    if ':' in host:
        try:
            return '%' not in host and ipaddress.IPv6Address(host) is not None
        except ValueError:
            return False
    try:
        ipaddress.IPv4Address(host)
        return True
    except ValueError:
        labels = host[:-1].split('.') if host.endswith('.') else host.split('.')
        return bool(labels) and all(re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', label)
                                    for label in labels)


def valid_client(issuer, client_id):
    if (not isinstance(issuer, str) or not issuer.startswith('https://') or
            any(ord(char) <= 0x20 or ord(char) == 0x7f for char in issuer) or
            re.search(r'%(?![0-9a-fA-F]{2})', issuer) or not isinstance(client_id, str)):
        return False
    try:
        parsed = urlsplit(issuer)
        host = parsed.hostname
    except (TypeError, ValueError):
        return False
    authority = parsed.netloc
    if (parsed.scheme != 'https' or not authority or parsed.username is not None or
            parsed.password is not None or not host or '#' in issuer or '?' in issuer):
        return False
    if authority.startswith('['):
        closing = authority.find(']')
        suffix = authority[closing + 1:] if closing >= 0 else None
        try:
            ipaddress.IPv6Address(host)
        except ValueError:
            return False
        if suffix is None or (suffix and re.fullmatch(r':[0-9]*', suffix) is None):
            return False
    else:
        if ('[' in authority or ']' in authority or authority.count(':') > 1 or
                '%' in authority):
            return False
        if ':' in authority:
            port = authority.rsplit(':', 1)[1]
            if port and re.fullmatch('[0-9]+', port) is None:
                return False
    return 0 < len(client_id) <= 256 and ascii_visible(client_id)


def validate_configuration(value, *, required=False):
    require(isinstance(value, dict) and set(value) == set(CONFIG_KEYS), 'PAIR_CONFIG_INVALID')
    require(all(isinstance(item, str) for item in value.values()), 'PAIR_CONFIG_INVALID')
    configured = [bool(value[name]) for name in CONFIG_KEYS]
    if not any(configured):
        require(not required, 'PAIR_CONFIG_REQUIRED')
        return dict(value)
    require(all(configured), 'PAIR_CONFIG_PARTIAL')
    require(valid_gateway(value['gateway']) and valid_client(value['issuer'], value['clientID']),
            'PAIR_CONFIG_INVALID')
    return dict(value)


def configuration_from_env(env=None, *, required=False):
    env = os.environ if env is None else env
    return validate_configuration({name: env.get(key, '') for name, key in CONFIG_KEYS.items()},
                                 required=required)


def quote_ldflags_arg(value):
    return '"' + value.replace('\\', '\\\\').replace('"', '\\"') + '"'


def linker_flags(configuration):
    configuration = validate_configuration(configuration)
    return ' '.join('-X ' + quote_ldflags_arg('main.' + BUILD_VARS[name] + '=' + configuration[name])
                    for name in CONFIG_KEYS)


def parse_build_info(output):
    if isinstance(output, bytes):
        try:
            output = output.decode('utf-8')
        except UnicodeError:
            raise PairBuildError('PAIR_BUILD_INFO_INVALID') from None
    if not isinstance(output, str) or len(output) > 64 * 1024:
        raise PairBuildError('PAIR_BUILD_INFO_INVALID')
    paths, flags = [], []
    for line in output.splitlines():
        entry = line.strip()
        if entry.startswith('path\t'):
            paths.append(entry[5:])
        elif entry.startswith('build\t-ldflags='):
            encoded = entry[len('build\t-ldflags='):]
            try:
                decoded = json.loads(encoded) if encoded.startswith('"') else encoded
            except (json.JSONDecodeError, TypeError):
                raise PairBuildError('PAIR_BUILD_INFO_INVALID') from None
            if not isinstance(decoded, str):
                raise PairBuildError('PAIR_BUILD_INFO_INVALID')
            flags.append(decoded)
    require(len(paths) == 1 and len(flags) == 1, 'PAIR_BUILD_INFO_INVALID')
    return {'path': paths[0], 'ldflags': flags[0]}


def verify_build_info(output, record):
    try:
        configuration = validate_configuration(record['pairConfig'])
        expected_flags = linker_flags(configuration)
        expected = {'path': ROLE, 'ldflags': expected_flags}
        require(record['ldflags'] == expected_flags and record['goBuildInfo'] == expected,
                'PAIR_BUILD_INFO_MISMATCH')
        actual = parse_build_info(output)
        require(actual == expected, 'PAIR_BUILD_INFO_MISMATCH')
    except (KeyError, TypeError):
        raise PairBuildError('PAIR_BUILD_INFO_MISMATCH') from None


def validate_record(record, revision, tree):
    fixed = {'schema': 'tether-mac-host-pair-build/1', 'status': 'blocked',
             'activation': 'blocked', 'distribution': 'blocked',
             'repository': 'O2-CONNECTIVE/Tether', 'sourceCommit': revision,
             'target': 'darwin-arm64', 'role': ROLE,
             'settings': {'GOVERSION': source.GO_VERSION, 'GOOS': 'darwin',
                          'GOARCH': 'arm64', 'CGO_ENABLED': '1'},
             'flags': list(FLAGS), 'helperExecutable': True, 'helperExecuted': False,
             'signingPerformed': False, 'blockers': list(BLOCKERS)}
    variable = {'sourceTree', 'buildUTC', 'pairConfig', 'ldflags', 'goBuildInfo',
                'snapshotGoFiles', 'locks', 'recipe', 'compiledRepositoryPackages', 'modules', 'image'}
    require(isinstance(record, dict) and set(record) == set(fixed) | variable and
            all(type(record.get(key)) is type(value) and record[key] == value
                for key, value in fixed.items()), 'PAIR_BUILD_RECORD_INVALID')
    require(record['sourceTree'] == tree and isinstance(tree, str) and
            re.fullmatch('[0-9a-f]{40}', tree) is not None, 'PAIR_BUILD_RECORD_INVALID')
    stamp = record['buildUTC']
    require(isinstance(stamp, str) and re.fullmatch(
            r'[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z', stamp),
            'PAIR_BUILD_RECORD_INVALID')
    try:
        datetime.strptime(stamp, '%Y-%m-%dT%H:%M:%SZ')
    except ValueError:
        raise PairBuildError('PAIR_BUILD_RECORD_INVALID') from None
    config = validate_configuration(record['pairConfig'])
    expected_ldflags = linker_flags(config)
    require(record['ldflags'] == expected_ldflags and
            record['goBuildInfo'] == {'path': ROLE, 'ldflags': expected_ldflags},
            'PAIR_BUILD_INFO_MISMATCH')
    inventory = product_files_for('darwin', True)
    snapshot = {str(PurePosixPath('backend') /
                    package[len(source.PROJECT):].lstrip('/') / name)
                for package, names in inventory.items() for name in names}
    def valid_hashes(value, paths):
        return (isinstance(value, dict) and set(value) == set(paths) and
                all(isinstance(item, str) and re.fullmatch('[0-9a-f]{64}', item)
                    for item in value.values()))
    require(valid_hashes(record['snapshotGoFiles'], snapshot) and
            valid_hashes(record['locks'], {'backend/go.mod', 'backend/go.sum'}) and
            valid_hashes(record['recipe'], RECIPE), 'PAIR_BUILD_RECORD_INVALID')
    packages = record['compiledRepositoryPackages']
    require(isinstance(packages, list) and all(isinstance(package, str) for package in packages) and
            len(packages) == len(set(packages)) and set(packages) <= set(inventory) and ROLE in packages,
            'PAIR_BUILD_RECORD_INVALID')
    modules = record['modules']
    require(isinstance(modules, list) and len(modules) <= 128, 'PAIR_BUILD_RECORD_INVALID')
    seen = set()
    for item in modules:
        require(isinstance(item, dict) and set(item) == {'Path', 'Version', 'Sum', 'GoModSum'} and
                all(isinstance(value, str) for value in item.values()), 'PAIR_BUILD_RECORD_INVALID')
        name = item['Path']
        require(name in licenses.MODULES and name not in seen and
                (item['Version'], item['Sum'], item['GoModSum']) == licenses.MODULES[name],
                'PAIR_BUILD_RECORD_INVALID')
        seen.add(name)
    artifact = record['image']
    require(isinstance(artifact, dict) and set(artifact) == {'path', 'sha256', 'bytes', 'kind', 'header'} and
            artifact['path'] == 'tether-host-pair' and
            isinstance(artifact['sha256'], str) and re.fullmatch('[0-9a-f]{64}', artifact['sha256']) and
            type(artifact['bytes']) is int and artifact['bytes'] >= 32 and
            artifact['kind'] == 'mach-o-executable' and isinstance(artifact['header'], dict),
            'PAIR_BUILD_RECORD_INVALID')


def verify_binary(path, record, *, cwd=None, env=None):
    require(inspect_image(path) == record['image'], 'PAIR_IMAGE_RECORD_MISMATCH')
    output = source.command(['go', 'version', '-m', str(path)],
                            cwd or source.ROOT / 'backend', env)
    verify_build_info(output, record)


def inspect_image(path):
    data = image.read_regular(path, LIMIT)
    header = image.macho(data, 'darwin-arm64')
    return {'path': 'tether-host-pair', 'sha256': source.digest(data), 'bytes': len(data),
            'kind': 'mach-o-executable', 'header': header}


def build(output, configuration=None, *, required_config=False):
    pair_config = configuration_from_env(required=required_config) if configuration is None else \
        validate_configuration(configuration, required=required_config)
    source.require(platform.system() == 'Darwin' and platform.machine() == 'arm64',
                   'BUILD_HOST_UNSUPPORTED')
    revision, tree = source.head()
    output = Path(os.path.abspath(output))
    source.require(not output.is_relative_to(source.ROOT), 'OUTPUT_INSIDE_SOURCE')
    source.require(output.parent.resolve(strict=True) == output.parent, 'OUTPUT_PARENT_INVALID')
    output.mkdir(mode=0o700)
    original = output.lstat()
    complete = False
    try:
        with tempfile.TemporaryDirectory(prefix='.compile-', dir=output) as temporary:
            stage = Path(temporary)
            files, locks, recipe = source.snapshot(revision, stage, RECIPE)
            module = stage / 'backend'
            env = source.environment(stage / 'cache', 'arm64')
            settings = source.build_metadata(module, env, 'arm64')
            source.source_inventory(env)
            packages, paths = source.closure(module, env, ROLE)
            modules = source.module_metadata(module, env, paths)
            expected_ldflags = linker_flags(pair_config)
            result = stage / 'tether-host-pair'
            source.command(['go', 'build', *FLAGS, '-ldflags', expected_ldflags,
                            '-o', str(result), ROLE], module, env)
            raw_build_info = source.command(['go', 'version', '-m', str(result)], module, env)
            actual_build_info = parse_build_info(raw_build_info)
            require(actual_build_info == {'path': ROLE, 'ldflags': expected_ldflags},
                    'PAIR_BUILD_INFO_MISMATCH')
            for relative, expected in (files | locks).items():
                source.require(source.digest((stage / relative).read_bytes()) == expected,
                               'BUILD_INPUT_CHANGED')
            source.require(source.head() == (revision, tree), 'SOURCE_CHANGED')
            artifact = inspect_image(result)
            record = {'schema': 'tether-mac-host-pair-build/1',
                      'status': 'blocked', 'activation': 'blocked', 'distribution': 'blocked',
                      'repository': 'O2-CONNECTIVE/Tether', 'sourceCommit': revision,
                      'sourceTree': tree, 'buildUTC': datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
                      'role': ROLE, 'target': 'darwin-arm64', 'settings': settings,
                      'flags': list(FLAGS), 'pairConfig': pair_config, 'ldflags': expected_ldflags,
                      'goBuildInfo': actual_build_info,
                      'snapshotGoFiles': files, 'locks': locks, 'recipe': recipe,
                      'compiledRepositoryPackages': packages, 'modules': modules, 'image': artifact,
                      'helperExecutable': True, 'helperExecuted': False, 'signingPerformed': False,
                      'blockers': list(BLOCKERS)}
            validate_record(record, revision, tree)
            result.chmod(0o600)
            result.rename(output / 'tether-host-pair')
            (output / 'BUILD_RECORD.json').write_text(json.dumps(record, indent=2) + '\n')
        complete = True
        return record
    finally:
        if not complete:
            current = output.lstat()
            if ((current.st_dev, current.st_ino) == (original.st_dev, original.st_ino)
                    and not output.is_symlink()):
                shutil.rmtree(output)


def main(argv=None):
    try:
        parser = source.Parser(prog='build_mac_host_pair', description=__doc__, allow_abbrev=False)
        parser.add_argument('--output', required=True, help='new output directory outside checkout')
        args = parser.parse_args(argv)
        record = build(args.output)
        print(json.dumps({'status': 'blocked', 'code': 'HOST_PAIR_BUILD_RECORDED',
                          'record': 'BUILD_RECORD.json', 'sourceCommit': record['sourceCommit'],
                          'imageSha256': record['image']['sha256'], 'blockers': list(BLOCKERS)}))
        return 2
    except (source.BuildError, PairBuildError) as error:
        print(json.dumps({'status': 'blocked', 'code': str(error)}))
    except (image.PackageError, OSError, ValueError, KeyError, UnicodeError):
        print(json.dumps({'status': 'blocked', 'code': 'PAIR_BUILD_INPUT_INVALID'}))
    return 1


if __name__ == '__main__':
    raise SystemExit(main())
