"""Compile a pinned resident library archive; every record remains blocked."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shutil
import stat
import subprocess
import tempfile

from go_ci import json_objects, product_files_for

ROOT = Path(__file__).resolve().parent.parent
PROJECT = 'github.com/O2-CONNECTIVE/rescue-host/backend'
ROLE = PROJECT + '/internal/residentpipe'
GO_VERSION = 'go1.27.3'
ARCHES = {'arm64': 'arm64', 'x86_64': 'amd64'}
RECIPE = ('scripts/build_mac_resident_source.py', 'scripts/go_ci.py')
BLOCKERS = (
    'PRODUCER_ENTRYPOINT_UNBOUND', 'POLICY_SELECTION_UNBOUND',
    'PUBLISHER_UNVERIFIED', 'PLACEMENT_UNVERIFIED', 'VERIFY_TO_EXEC_UNVERIFIED',
    'TOOLCHAIN_SDK_INTEGRITY_UNVERIFIED', 'RUNTIME_COMPATIBILITY_UNVERIFIED',
    'LICENSE_ARTIFACT_CONTENTS_UNAUDITED',
)
COMPILE_FLAGS = ('-mod=readonly', '-trimpath', '-buildvcs=false', '-buildmode=archive', '-p=1')
LIMIT = 64 * 1024 * 1024
EXTRA_INPUTS = ('CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles',
                'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles')


class BuildError(Exception):
    pass


def require(condition, code):
    if not condition:
        raise BuildError(code)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def command(args, cwd, env=None):
    # Retain/wait the original compiler on interruption. Killing only the Go
    # driver on a timeout could abandon its compiler children. No process/PID
    # supplied by an input is ever signaled, retried, or replaced here.
    interrupted = False
    try:
        with subprocess.Popen(args, cwd=cwd, env=env, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE) as process:
            while True:
                try:
                    stdout, _ = process.communicate()
                    break
                except KeyboardInterrupt:
                    interrupted = True
            require(not interrupted, 'BUILD_INTERRUPTED')
            require(process.returncode == 0, 'BUILD_COMMAND_FAILED')
    except OSError:
        raise BuildError('BUILD_COMMAND_FAILED') from None
    require(len(stdout) <= LIMIT, 'METADATA_TOO_LARGE')
    return stdout


def git(*args):
    return command(['git', '-c', 'core.fsmonitor=false', *args], ROOT)


def head():
    revision = git('rev-parse', 'HEAD').decode('ascii').strip()
    tree = git('rev-parse', 'HEAD^{tree}').decode('ascii').strip()
    require(re.fullmatch('[0-9a-f]{40}', revision) is not None and
            re.fullmatch('[0-9a-f]{40}', tree) is not None, 'SOURCE_ID_INVALID')
    require(Path(git('rev-parse', '--show-toplevel').decode().strip()).resolve() == ROOT,
            'SOURCE_ROOT_INVALID')
    require(not git('status', '--porcelain=v1', '--untracked-files=all').strip(),
            'SOURCE_DIRTY')
    return revision, tree


def source_blob(revision, name):
    require(re.fullmatch(r'[A-Za-z0-9_./-]+', name) is not None and
            '..' not in PurePosixPath(name).parts and not PurePosixPath(name).is_absolute(),
            'SOURCE_PATH_INVALID')
    metadata = git('ls-tree', revision, '--', name).decode().strip()
    require(metadata.startswith('100644 blob ') or metadata.startswith('100755 blob '),
            'SOURCE_FILE_INVALID')
    data = git('show', revision + ':' + name)
    require(len(data) <= 4 * 1024 * 1024, 'SOURCE_FILE_INVALID')
    return data


def snapshot(revision, destination):
    """Only the reviewed Darwin/cgo Go inventory, from exact Git blobs."""
    inventory = product_files_for('darwin', True)
    files = {}
    for package, names in sorted(inventory.items()):
        require(package.startswith(PROJECT), 'INVENTORY_INVALID')
        suffix = package[len(PROJECT):].lstrip('/')
        for name in sorted(names):
            require(re.fullmatch(r'[A-Za-z0-9_]+\.go', name) is not None,
                    'INVENTORY_INVALID')
            relative = str(PurePosixPath('backend') / suffix / name)
            data = source_blob(revision, relative)
            path = destination / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
            files[relative] = digest(data)
    locks = {}
    for name in ('go.mod', 'go.sum'):
        relative = 'backend/' + name
        data = source_blob(revision, relative)
        (destination / relative).write_bytes(data)
        locks[relative] = digest(data)
    recipe = {name: digest(source_blob(revision, name)) for name in RECIPE}
    # Imported inventory and executing recipe must be the pinned checked-out ones.
    for name, expected in recipe.items():
        require(digest((ROOT / name).read_bytes()) == expected, 'RECIPE_CHANGED')
    return files, locks, recipe


def environment(cache, arch):
    # No downloads, toolchain acquisition, user GOFLAGS, replacement workspace,
    # cgo flag injection, normal cache changes, or runtime configuration.
    allowed = ('PATH', 'HOME', 'TMPDIR', 'GOMODCACHE')
    env = {name: os.environ[name] for name in allowed if name in os.environ}
    env.update(GOENV='off', GOTOOLCHAIN='local', GOWORK='off', GOPROXY='off',
               GOSUMDB='off', GOFLAGS='', GOOS='darwin', GOARCH=arch,
               CGO_ENABLED='1', GOCACHE=str(cache))
    return env


def build_metadata(module, env, arch):
    raw = command(['go', 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH', 'CGO_ENABLED'],
                  module, env)
    settings = json.loads(raw)
    require(settings == {'GOVERSION': GO_VERSION, 'GOOS': 'darwin',
                         'GOARCH': arch, 'CGO_ENABLED': '1'}, 'TOOLCHAIN_MISMATCH')
    command(['go', 'mod', 'verify'], module, env)
    return settings


def module_metadata(module, env, paths):
    modules = []
    mains = 0
    seen = set()
    for item in json_objects(command(['go', 'list', '-mod=readonly', '-m', '-json', PROJECT, *paths],
                                     module, env).decode()):
        require(not item.get('Replace'), 'MODULE_REPLACEMENT')
        require(item.get('Path') not in seen, 'MODULE_INVALID')
        seen.add(item.get('Path'))
        if item.get('Main'):
            mains += 1
            require(item.get('Path') == PROJECT, 'MODULE_INVALID')
        else:
            require(all(isinstance(item.get(key), str) and item[key]
                        for key in ('Path', 'Version', 'Sum', 'GoModSum')), 'MODULE_INVALID')
            modules.append({key: item[key] for key in ('Path', 'Version', 'Sum', 'GoModSum')})
    require(mains == 1 and seen == {PROJECT, *paths}, 'MODULE_INVALID')
    return sorted(modules, key=lambda item: item['Path'])


def go_files(package):
    require(not any(package.get(key) for key in EXTRA_INPUTS), 'EXTRA_BUILD_INPUT')
    return set(package.get('GoFiles', [])) | set(package.get('CgoFiles', []))


def source_inventory(env):
    # Do not silently omit a newly committed production file when preparing the
    # whitelist snapshot. The real source selection must match all 45 roles.
    actual = {}
    raw = command(['go', 'list', '-mod=readonly', '-json', './...'], ROOT / 'backend', env)
    for package in json_objects(raw.decode()):
        require(not package.get('Module', {}).get('Replace'), 'MODULE_REPLACEMENT')
        actual[package['ImportPath']] = go_files(package)
    require(actual == product_files_for('darwin', True), 'INVENTORY_INVALID')


def closure(module, env):
    """Confirm the fixed package really uses the reviewed snapshot selections."""
    selected = []
    paths = set()
    expected = product_files_for('darwin', True)
    raw = command(['go', 'list', '-mod=readonly', '-deps', '-json', ROLE], module, env)
    for package in json_objects(raw.decode()):
        path = package.get('ImportPath', '')
        info = package.get('Module', {})
        require(not info.get('Replace'), 'MODULE_REPLACEMENT')
        if not path.startswith(PROJECT):
            if info:
                name = info.get('Path')
                require(isinstance(name, str) and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9./_+~-]*', name)
                        and '.' in name.split('/')[0] and '..' not in name.split('/')
                        and not info.get('Main') and (path == name or path.startswith(name + '/')), 'MODULE_INVALID')
                paths.add(name)
            else:
                require(package.get('Standard'), 'MODULE_INVALID')
            continue
        require(info.get('Main') is True and info.get('Path') == PROJECT, 'MODULE_INVALID')
        require(path in expected, 'INVENTORY_INVALID')
        require(go_files(package) == expected[path], 'INVENTORY_INVALID')
        require(Path(package['Dir']).resolve() ==
                (module / path[len(PROJECT):].lstrip('/')).resolve(), 'SOURCE_ROOT_INVALID')
        selected.append(path)
    require(ROLE in selected, 'INVENTORY_INVALID')
    return sorted(selected), sorted(paths)


def archive(path):
    before = path.lstat()
    require(stat.S_ISREG(before.st_mode) and before.st_size <= LIMIT, 'ARCHIVE_INVALID')
    with path.open('rb') as stream:
        opened = os.fstat(stream.fileno())
        require((before.st_dev, before.st_ino) == (opened.st_dev, opened.st_ino), 'ARCHIVE_INVALID')
        data = stream.read(LIMIT + 1)
        after = os.fstat(stream.fileno())
    current = path.lstat()
    require(len(data) <= LIMIT and data.startswith(b'!<arch>\n') and
            (opened.st_dev, opened.st_ino, opened.st_size, opened.st_mtime_ns) ==
            (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns) ==
            (current.st_dev, current.st_ino, current.st_size, current.st_mtime_ns), 'ARCHIVE_INVALID')
    return {'sha256': digest(data), 'bytes': len(data), 'kind': 'go-library-archive'}


def build(output):
    require(platform.system() == 'Darwin' and platform.machine() in ARCHES,
            'BUILD_HOST_UNSUPPORTED')
    arch = ARCHES[platform.machine()]
    revision, tree = head()
    output = Path(os.path.abspath(output))
    require(not output.is_relative_to(ROOT), 'OUTPUT_INSIDE_SOURCE')
    require(output.parent.resolve(strict=True) == output.parent, 'OUTPUT_PARENT_INVALID')
    output.mkdir(mode=0o700)  # Never replace an existing output or symlink.
    original = output.lstat()
    completed = False
    try:
        with tempfile.TemporaryDirectory(prefix='.compile-', dir=output) as temporary:
            stage = Path(temporary)
            files, locks, recipe = snapshot(revision, stage)
            module = stage / 'backend'
            env = environment(stage / 'cache', arch)
            settings = build_metadata(module, env, arch)
            source_inventory(env)
            packages, paths = closure(module, env)
            modules = module_metadata(module, env, paths)
            name = 'residentpipe-darwin-' + arch + '.a'
            result = stage / name
            flags = ['build', *COMPILE_FLAGS, '-o', str(result), ROLE]
            command(['go', *flags], module, env)
            for relative, expected in (files | locks).items():
                require(digest((stage / relative).read_bytes()) == expected, 'BUILD_INPUT_CHANGED')
            require(head() == (revision, tree), 'SOURCE_CHANGED')
            artifact = archive(result)
            artifact['path'] = name
            record = {'schema': 'rescue-mac-resident-library-build/1',
                      'status': 'blocked', 'activation': 'blocked', 'distribution': 'blocked',
                      'repository': 'O2-CONNECTIVE/rescue-host', 'sourceCommit': revision,
                      'sourceTree': tree, 'role': ROLE, 'settings': settings,
                      'flags': list(COMPILE_FLAGS),
                      'snapshotGoFiles': files, 'locks': locks, 'recipe': recipe,
                      'compiledRepositoryPackages': packages, 'modules': modules,
                      'archive': artifact, 'producerExecutable': False,
                      'cryptographicAttestation': False, 'blockers': list(BLOCKERS)}
            result.chmod(0o600)
            result.rename(output / name)
            (output / 'BUILD_RECORD.json').write_text(json.dumps(record, indent=2) + '\n')
        completed = True
        return record
    finally:
        if not completed:
            # Only this newly created output is eligible for failure cleanup.
            current = output.lstat()
            if (current.st_dev, current.st_ino) == (original.st_dev, original.st_ino) and not output.is_symlink():
                shutil.rmtree(output)


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise BuildError('ARGUMENT_INVALID')


def main(argv=None):
    try:
        parser = Parser(prog='build_mac_resident_source', description=__doc__, allow_abbrev=False)
        parser.add_argument('--output', required=True, help='new output directory outside the checkout')
        args = parser.parse_args(argv)
        record = build(args.output)
        print(json.dumps({'status': 'blocked', 'code': 'LIBRARY_BUILD_RECORDED',
                          'record': 'BUILD_RECORD.json', 'sourceCommit': record['sourceCommit'],
                          'archiveSha256': record['archive']['sha256'],
                          'blockers': list(BLOCKERS)}))
        return 2  # A successful build record never grants activation/distribution.
    except BuildError as error:
        print(json.dumps({'status': 'blocked', 'code': str(error)}))
    except (OSError, ValueError, KeyError, UnicodeError):
        print(json.dumps({'status': 'blocked', 'code': 'BUILD_INPUT_INVALID'}))
    return 1


if __name__ == '__main__':
    raise SystemExit(main())
