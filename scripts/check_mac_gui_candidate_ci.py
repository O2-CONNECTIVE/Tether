"""Verify the fixed nonexecuting GUI build and its actual assembled bytes."""
import json
from pathlib import Path
import plistlib
import sys

import build_mac_gui_candidate as builder
import check_mac_sealed_producer_ci as sealed_check


def validate(record, revision, tree):
    expected = {'schema': 'tether-mac-gui-candidate-build/2', 'repository': 'O2-CONNECTIVE/Tether',
                'status': 'blocked', 'activation': 'blocked', 'distribution': 'blocked',
                'sourceCommit': revision, 'sourceTree': tree, 'target': 'darwin-arm64',
                'app': builder.support.APP, 'cargoFlags': list(builder.support.CARGO_FLAGS),
                'rustCfg': ['resident_native_source'], 'swiftFixtureDefines': [],
                'appAssembled': True, 'imageExecuted': False, 'imageLoaded': False,
                'producerIncluded': True, 'signingPerformed': False, 'enrollmentAccessed': False,
                'productionGateClosed': True, 'cryptographicAttestation': False,
                'blockers': list(builder.support.BLOCKERS)}
    for key, value in expected.items():
        if type(record.get(key)) is not type(value) or record[key] != value:
            raise ValueError('blocked GUI build record mismatch')
    if set(record['images']) != {'gui', 'native', 'producer'} or set(record['recipe']) != set(builder.support.RECIPE):
        raise ValueError('GUI build inventory mismatch')
    producer = record['producerBuild']
    sealed_check.validate_result(2, sealed_check.builder.summary(producer), producer, revision, tree)
    if record['images']['producer'] != {'path': builder.layout.ASSEMBLY_IMAGES['producer'],
            'sha256': producer['image']['sha256'], 'bytes': producer['image']['bytes']}:
        raise ValueError('GUI and producer material mismatch')


def run():
    if len(sys.argv) != 2:
        raise ValueError('output required')
    output = Path(sys.argv[1])
    record = builder.build(output)
    revision, tree = builder.source.head()
    validate(record, revision, tree)
    persisted = json.loads((output / 'BUILD_RECORD.json').read_text())
    if persisted != record:
        raise ValueError('GUI record persistence mismatch')
    app = output / record['app']
    expected_files = {'Contents/Info.plist', *builder.layout.ASSEMBLY_IMAGES.values()}
    actual_files = {p.relative_to(app).as_posix() for p in app.rglob('*') if p.is_file()}
    if actual_files != expected_files:
        raise ValueError('assembled GUI file inventory mismatch')
    for role, relative in builder.layout.ASSEMBLY_IMAGES.items():
        data = builder.layout.files.read_regular(app / relative, builder.layout.LIMIT)
        if record['images'][role] != {'path': relative, 'sha256': builder.source.digest(data), 'bytes': len(data)}:
            raise ValueError('assembled GUI image mismatch')
        if (app / relative).stat().st_mode & 0o111:
            raise ValueError('retained GUI image is executable')
    if builder.source.digest((app / 'Contents/Info.plist').read_bytes()) != record['infoPlistSha256']:
        raise ValueError('assembled GUI plist mismatch')
    matched = builder.layout.verify_assembly(app, 'darwin-arm64', builder.summary(record)['imageHashes'])
    if matched != record['linkLayout']:
        raise ValueError('actual GUI link layout mismatch')
    plist = plistlib.loads((app / 'Contents/Info.plist').read_bytes())
    if plist['LSMinimumSystemVersion'] != matched['minimumMacOSDeclared']:
        raise ValueError('actual GUI minimum OS mismatch')
    print(json.dumps({'check': 'mac-gui-candidate-build', **builder.summary(record),
                      'appAssembled': True, 'imageExecuted': False, 'producerIncluded': True,
                      'productionGateClosed': True}))


if __name__ == '__main__':
    try:
        run()
    except (builder.source.BuildError, builder.layout.LayoutError) as error:
        print('MAC_GUI_CANDIDATE_BUILD_FAILED ' + str(error), file=sys.stderr)
        raise SystemExit(1)
    except (OSError, ValueError, KeyError, TypeError, builder.layout.files.PackageError):
        print('MAC_GUI_CANDIDATE_BUILD_FAILED', file=sys.stderr)
        raise SystemExit(1)
