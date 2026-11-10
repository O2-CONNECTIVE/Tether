"""Build/sign a fixed local Mac candidate using an explicitly approved existing identity.

This command signs code. It never imports credentials, notarizes, installs or launches.
"""
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import plistlib
import shutil
import tempfile

import mac_signed_release_support as release
from mac_signed_release_support import support, source, layout

APP = 'Tether-Signed-Candidate.app'


def build(output, team, certificate, revision):
    release.identity(team, certificate)
    source.require(platform.system() == 'Darwin' and platform.machine() == 'arm64' and
                   platform.mac_ver()[0].split('.')[0].isdigit() and
                   int(platform.mac_ver()[0].split('.')[0]) >= 27, 'SIGNED_RELEASE_HOST_REQUIRED')
    actual_revision, tree = source.head()
    source.require(actual_revision == revision, 'APPROVED_SOURCE_MISMATCH')
    output = Path(os.path.abspath(output))
    source.require(not output.is_relative_to(source.ROOT), 'OUTPUT_INSIDE_SOURCE')
    source.require(output.parent.resolve(strict=True) == output.parent, 'OUTPUT_PARENT_INVALID')
    output.mkdir(mode=0o700)
    original, complete = output.lstat(), False
    try:
        with tempfile.TemporaryDirectory(prefix='.compile-', dir=output) as temporary:
            stage = Path(temporary)
            files, recipe = release.snapshot(revision, stage)
            env = support.environment(stage)
            env['MACOSX_DEPLOYMENT_TARGET'] = '27.0'
            swift, sdk, settings = support.toolchain(stage, env)
            settings['deploymentTarget'] = '27.0'
            observer = release.compile_observer(stage, env, swift, sdk)
            producer, producer_record = support.build_producer(stage, revision, tree)
            before = {'producer': release.image_record(producer, layout.ASSEMBLY_IMAGES['producer'])}
            producer.chmod(0o700)
            release.sign(producer, 'producer', certificate, stage, env)
            signed_producer = release.image_record(producer, layout.ASSEMBLY_IMAGES['producer'])
            approved = release.validate_observation(
                release.observe(observer, producer, 'producer', team, certificate, stage, env),
                'producer', team, certificate)
            release.unchanged(producer, signed_producer)
            generated = release.provider_bytes(approved)
            provider = stage / release.PROVIDER
            provider.parent.mkdir()
            provider.write_bytes(generated)
            generated_hash = source.digest(generated)
            built = support.compile_images(stage, env, swift, sdk, signed_release=True)
            release.unchanged(producer, signed_producer)
            source.require(provider.read_bytes() == generated, 'GENERATED_IDENTITY_CHANGED')
            release.verify_inputs(stage, files, revision, tree, recipe)
            built['producer'] = producer
            app = output / APP
            minima = [27 << 16]
            for role, relative in layout.ASSEMBLY_IMAGES.items():
                data = layout.files.read_regular(built[role], layout.LIMIT)
                minima.append(layout.inspect(data, 'darwin-arm64', role))
                if role != 'producer':
                    before[role] = release.image_record(built[role], relative)
                destination = app / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(data)
                destination.chmod(0o700)
            # Refuse source config changes, while the signed candidate declares macOS 27+.
            support.plist_config(stage, layout.files.version_string(max(minima)))
            plist = plistlib.dumps(release.signed_plist(layout.files.version_string(max(minima))), sort_keys=True)
            (app / 'Contents/Info.plist').write_bytes(plist)
            release.sign(app / layout.ASSEMBLY_IMAGES['native'], 'native', certificate, stage, env)
            signed_native = release.image_record(app / layout.ASSEMBLY_IMAGES['native'], layout.ASSEMBLY_IMAGES['native'])
            release.sign(app, 'gui', certificate, stage, env)
            source.require(release.image_record(app / layout.ASSEMBLY_IMAGES['native'], layout.ASSEMBLY_IMAGES['native']) ==
                           signed_native, 'SIGNED_NATIVE_CHANGED')
            release.unchanged(app / layout.ASSEMBLY_IMAGES['producer'], signed_producer)
            signatures, images = {}, {}
            for role, relative in layout.ASSEMBLY_IMAGES.items():
                path = app if role == 'gui' else app / relative
                bound = approved['cdhash'] if role == 'producer' else None
                signatures[role] = release.validate_observation(
                    release.observe(observer, path, role, team, certificate, stage, env, cdhash=bound),
                    role, team, certificate, bound)
                images[role] = release.image_record(app / relative, relative)
            release.unchanged(app / layout.ASSEMBLY_IMAGES['producer'], signed_producer)
            source.require(images['native'] == signed_native, 'SIGNED_NATIVE_CHANGED')
            matched = release.inventory(app, {role: value['sha256'] for role, value in images.items()})
            source.require((app / 'Contents/Info.plist').read_bytes() == plist, 'SIGNED_PLIST_CHANGED')
            release.verify_inputs(stage, files, revision, tree, recipe)
            source.require(provider.read_bytes() == generated, 'GENERATED_IDENTITY_CHANGED')
            evidence = output / release.PROVIDER
            evidence.parent.mkdir()
            evidence.write_bytes(generated)
            record = {'schema': 'tether-mac-signed-candidate-build/1', 'repository': 'O2-CONNECTIVE/Tether',
                      'status': 'signed-local-candidate', 'activation': 'unverified', 'distribution': 'blocked',
                      'sourceCommit': revision, 'sourceTree': tree, 'target': 'darwin-arm64',
                      'buildUTC': datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
                      'app': APP, 'settings': settings, 'snapshotFiles': files, 'recipe': recipe,
                      'producerBuild': producer_record, 'preSigningImages': before, 'images': images,
                      'approvedProducer': approved, 'signatures': signatures, 'signedInventory': matched,
                      'generatedIdentity': {'path': release.PROVIDER, 'sha256': generated_hash,
                                            'source': 'validated-producer-signature'},
                      'swiftDefines': ['TETHER_SIGNED_RELEASE'], 'rustCfg': ['resident_native_source'],
                      'signingPerformed': True, 'notarizationPerformed': False, 'imageExecuted': False,
                      'imageLoaded': False, 'enrollmentAccessed': False, 'blockers': list(release.BLOCKERS)}
            (output / 'BUILD_RECORD.json').write_text(json.dumps(record, indent=2) + '\n')
        complete = True
        return record
    finally:
        if not complete:
            current = output.lstat()
            if (current.st_dev, current.st_ino) == (original.st_dev, original.st_ino) and not output.is_symlink():
                shutil.rmtree(output)


def main(argv=None):
    try:
        parser = source.Parser(description=__doc__, allow_abbrev=False)
        for name in ('output', 'team-id', 'certificate-sha1', 'source-commit'):
            parser.add_argument('--' + name, required=True)
        args = parser.parse_args(argv)
        record = build(args.output, args.team_id, args.certificate_sha1, args.source_commit)
        print(json.dumps({'status': record['status'], 'code': 'SIGNED_LOCAL_CANDIDATE_RECORDED',
                          'record': 'BUILD_RECORD.json', 'sourceCommit': record['sourceCommit'],
                          'distribution': 'blocked', 'blockers': list(release.BLOCKERS)}))
        return 2  # Local signature validity is not distribution/connection acceptance.
    except (source.BuildError, layout.LayoutError, layout.files.PackageError) as error:
        print(json.dumps({'status': 'refused', 'code': str(error)}))
    except (OSError, ValueError, TypeError, KeyError, UnicodeError):
        print(json.dumps({'status': 'refused', 'code': 'SIGNED_RELEASE_INPUT_INVALID'}))
    return 1


if __name__ == '__main__':
    raise SystemExit(main())
