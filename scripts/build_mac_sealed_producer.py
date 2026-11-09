"""Build the fixed enrolled producer image; never execute, sign or distribute it."""
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import shutil
import tempfile

import build_mac_resident_source as source
import package_mac_agent as image

ROLE = source.PROJECT + '/cmd/resident-pipe'
FLAGS = ('-mod=readonly', '-trimpath', '-buildvcs=false', '-buildmode=exe', '-p=1')
RECIPE = ('scripts/build_mac_sealed_producer.py', 'scripts/build_mac_resident_source.py',
          'scripts/go_ci.py', 'scripts/package_mac_agent.py', 'scripts/collect_go_licenses.py')
BLOCKERS = ('GUI_BUILD_UNBOUND', 'RELEASE_IDENTITY_UNCONFIGURED', 'SIGNING_UNVERIFIED',
            'ENROLLMENT_UNCONFIGURED', 'PLACEMENT_UNVERIFIED', 'VERIFY_TO_EXEC_UNVERIFIED',
            'TOOLCHAIN_SDK_INTEGRITY_UNVERIFIED', 'RUNTIME_COMPATIBILITY_UNVERIFIED',
            'NOTICE_CLOSURE_UNAUDITED', 'BINARY_SECRET_PATH_AUDIT_PENDING')


def inspect_image(path):
    data = image.read_regular(path, source.LIMIT)
    header = image.macho(data, 'darwin-arm64')
    return {'path': 'resident-pipe', 'sha256': source.digest(data), 'bytes': len(data),
            'kind': 'mach-o-executable', 'header': header}


def build(output):
    source.require(platform.system() == 'Darwin' and platform.machine() == 'arm64',
                   'BUILD_HOST_UNSUPPORTED')
    revision, tree = source.head()
    output = Path(os.path.abspath(output))
    source.require(not output.is_relative_to(source.ROOT), 'OUTPUT_INSIDE_SOURCE')
    source.require(output.parent.resolve(strict=True) == output.parent, 'OUTPUT_PARENT_INVALID')
    output.mkdir(mode=0o700)  # New output only; no existing image is replaced.
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
            result = stage / 'resident-pipe'
            source.command(['go', 'build', *FLAGS, '-o', str(result), ROLE], module, env)
            for relative, expected in (files | locks).items():
                source.require(source.digest((stage / relative).read_bytes()) == expected,
                               'BUILD_INPUT_CHANGED')
            source.require(source.head() == (revision, tree), 'SOURCE_CHANGED')
            artifact = inspect_image(result)
            record = {'schema': 'tether-mac-sealed-producer-build/1',
                      'status': 'blocked', 'activation': 'blocked', 'distribution': 'blocked',
                      'repository': 'O2-CONNECTIVE/Tether', 'sourceCommit': revision,
                      'sourceTree': tree, 'buildUTC': datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
                      'role': ROLE, 'target': 'darwin-arm64', 'settings': settings,
                      'flags': list(FLAGS), 'snapshotGoFiles': files, 'locks': locks, 'recipe': recipe,
                      'compiledRepositoryPackages': packages, 'modules': modules, 'image': artifact,
                      'producerExecutable': True, 'producerExecuted': False,
                      'signingPerformed': False, 'enrollmentAccessed': False,
                      'cryptographicAttestation': False, 'blockers': list(BLOCKERS)}
            # This is a retained build product, not an installed executable.
            result.chmod(0o600)
            result.rename(output / 'resident-pipe')
            (output / 'BUILD_RECORD.json').write_text(json.dumps(record, indent=2) + '\n')
        complete = True
        return record
    finally:
        if not complete:
            current = output.lstat()
            if ((current.st_dev, current.st_ino) == (original.st_dev, original.st_ino)
                    and not output.is_symlink()):
                shutil.rmtree(output)


def summary(record):
    return {'status': 'blocked', 'code': 'SEALED_PRODUCER_BUILD_RECORDED',
            'record': 'BUILD_RECORD.json', 'sourceCommit': record['sourceCommit'],
            'imageSha256': record['image']['sha256'], 'blockers': list(BLOCKERS)}


def main(argv=None):
    try:
        parser = source.Parser(prog='build_mac_sealed_producer', description=__doc__, allow_abbrev=False)
        parser.add_argument('--output', required=True, help='new output directory outside checkout')
        args = parser.parse_args(argv)
        print(json.dumps(summary(build(args.output))))
        return 2
    except source.BuildError as error:
        print(json.dumps({'status': 'blocked', 'code': str(error)}))
    except image.PackageError:
        print(json.dumps({'status': 'blocked', 'code': 'PRODUCER_IMAGE_INVALID'}))
    except (OSError, ValueError, KeyError, UnicodeError):
        print(json.dumps({'status': 'blocked', 'code': 'BUILD_INPUT_INVALID'}))
    return 1


if __name__ == '__main__':
    raise SystemExit(main())
