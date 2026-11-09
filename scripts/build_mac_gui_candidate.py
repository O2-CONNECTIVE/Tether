"""Build and assemble a blocked GUI/native app on a disposable CI Mac; never run it."""
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import plistlib
import shutil
import tempfile

import mac_gui_build_support as support
from mac_gui_build_support import source, layout


def build(output):
    source.require(platform.system() == 'Darwin' and platform.machine() == 'arm64' and
                   os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted', 'GUI_CI_HOST_REQUIRED')
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
            files, recipe = support.snapshot(revision, stage)
            env = support.environment(stage)
            swift, sdk, settings = support.toolchain(stage, env)
            built = support.compile_images(stage, env, swift, sdk)
            app = output / support.APP
            images = {}
            for role, relative in layout.IMAGES.items():
                data = layout.files.read_regular(built[role], layout.LIMIT)
                layout.inspect(data, 'darwin-arm64', role)
                destination = app / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(data)
                destination.chmod(0o600)  # Inert build products, not an installed app.
                images[role] = {'path': relative, 'sha256': source.digest(data), 'bytes': len(data)}
            plist = plistlib.dumps(support.plist_config(stage), sort_keys=True)
            (app / 'Contents/Info.plist').write_bytes(plist)
            matched = layout.verify(app, 'darwin-arm64', {r: v['sha256'] for r, v in images.items()})
            for name, expected in files.items():
                source.require(source.digest((stage / name).read_bytes()) == expected, 'BUILD_INPUT_CHANGED')
            source.require(source.head() == (revision, tree), 'SOURCE_CHANGED')
            record = {'schema': 'tether-mac-gui-candidate-build/1', 'repository': 'O2-CONNECTIVE/Tether',
                      'status': 'blocked', 'activation': 'blocked', 'distribution': 'blocked',
                      'sourceCommit': revision, 'sourceTree': tree, 'target': 'darwin-arm64',
                      'buildUTC': datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
                      'app': support.APP, 'settings': settings, 'cargoFlags': list(support.CARGO_FLAGS),
                      'rustCfg': ['resident_native_source'], 'swiftFixtureDefines': [],
                      'snapshotFiles': files, 'recipe': recipe, 'images': images,
                      'infoPlistSha256': source.digest(plist), 'linkLayout': matched,
                      'appAssembled': True, 'imageExecuted': False, 'imageLoaded': False,
                      'producerIncluded': False, 'signingPerformed': False,
                      'enrollmentAccessed': False, 'productionGateClosed': True,
                      'cryptographicAttestation': False, 'blockers': list(support.BLOCKERS)}
            (output / 'BUILD_RECORD.json').write_text(json.dumps(record, indent=2) + '\n')
        complete = True
        return record
    finally:
        if not complete:
            current = output.lstat()
            if (current.st_dev, current.st_ino) == (original.st_dev, original.st_ino) and not output.is_symlink():
                shutil.rmtree(output)


def summary(record):
    return {'status': 'blocked', 'code': 'GUI_CANDIDATE_BUILD_RECORDED',
            'record': 'BUILD_RECORD.json', 'sourceCommit': record['sourceCommit'],
            'imageHashes': {r: v['sha256'] for r, v in record['images'].items()},
            'blockers': list(support.BLOCKERS)}


def main(argv=None):
    try:
        parser = source.Parser(prog='build_mac_gui_candidate', description=__doc__, allow_abbrev=False)
        parser.add_argument('--output', required=True, help='new output directory outside checkout')
        args = parser.parse_args(argv)
        print(json.dumps(summary(build(args.output))))
        return 2
    except (source.BuildError, layout.LayoutError) as error:
        print(json.dumps({'status': 'blocked', 'code': str(error)}))
    except (OSError, ValueError, KeyError, TypeError, UnicodeError, layout.files.PackageError):
        print(json.dumps({'status': 'blocked', 'code': 'GUI_BUILD_INPUT_INVALID'}))
    return 1


if __name__ == '__main__':
    raise SystemExit(main())
