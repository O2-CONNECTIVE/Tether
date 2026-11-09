"""Check one actual sealed producer build without executing or uploading its image."""
import json
import os
from pathlib import Path
import platform
import sys

import build_mac_sealed_producer as builder
from check_mac_enrollment_lookup import completed


def validate_result(status, output, record, revision, tree):
    expected = {'schema': 'tether-mac-sealed-producer-build/1', 'repository': 'O2-CONNECTIVE/Tether',
                'status': 'blocked', 'activation': 'blocked', 'distribution': 'blocked',
                'sourceCommit': revision, 'sourceTree': tree, 'target': 'darwin-arm64',
                'role': builder.ROLE, 'flags': list(builder.FLAGS),
                'settings': {'GOVERSION': 'go1.27.3', 'GOOS': 'darwin', 'GOARCH': 'arm64', 'CGO_ENABLED': '1'},
                'producerExecutable': True, 'producerExecuted': False, 'signingPerformed': False,
                'enrollmentAccessed': False, 'cryptographicAttestation': False,
                'blockers': list(builder.BLOCKERS)}
    if status != 2 or output != builder.summary(record):
        raise ValueError('blocked build summary mismatch')
    for key, value in expected.items():
        if type(record.get(key)) is not type(value) or record[key] != value:
            raise ValueError('blocked build record mismatch')
    if (record['image']['path'] != 'resident-pipe' or record['image']['kind'] != 'mach-o-executable'
            or builder.ROLE not in record['compiledRepositoryPackages']
            or set(record['recipe']) != set(builder.RECIPE)):
        raise ValueError('fixed executable source role mismatch')


def run():
    if len(sys.argv) != 2 or sys.platform != 'darwin' or platform.machine() != 'arm64':
        raise ValueError('unsupported sealed build verification')
    output = Path(sys.argv[1])
    env = {k: v for k, v in os.environ.items()
           if k in ('PATH', 'HOME', 'TMPDIR', 'GOMODCACHE', 'DEVELOPER_DIR')}
    result = completed([sys.executable, str(builder.source.ROOT / 'scripts/build_mac_sealed_producer.py'),
                        '--output', str(output)], env, builder.source.ROOT)
    if result.returncode != 2:
        raise ValueError('sealed build did not produce a blocked record')
    record = json.loads((output / 'BUILD_RECORD.json').read_text())
    revision, tree = builder.source.head()
    validate_result(result.returncode, json.loads(result.stdout), record, revision, tree)
    if builder.inspect_image(output / 'resident-pipe') != record['image']:
        raise ValueError('recorded image mismatch')
    print(json.dumps({'fixture': 'sealed-producer-source-build', 'status': 'blocked',
                      'activation': 'blocked', 'distribution': 'blocked',
                      'sourceCommit': revision, 'sourceTree': tree,
                      'target': 'darwin-arm64', 'builderExit': 2,
                      'imageSha256': record['image']['sha256'], 'producerExecuted': False,
                      'signingPerformed': False, 'enrollmentAccessed': False,
                      'cryptographicAttestation': False}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError, TypeError, builder.source.BuildError, builder.image.PackageError):
        print('MAC_SEALED_PRODUCER_BUILD_FAILED', file=sys.stderr)
        raise SystemExit(1)
