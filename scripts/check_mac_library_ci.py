"""Verify the documented blocked source-build result; never authorize launch."""
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys

import build_mac_resident_source as builder
from check_mac_enrollment_lookup import completed


def validate_result(status, summary, record, revision, tree, arch):
    expected = {'status': 'blocked', 'code': 'LIBRARY_BUILD_RECORDED',
                'record': 'BUILD_RECORD.json', 'sourceCommit': revision,
                'archiveSha256': record['archive']['sha256'], 'blockers': list(builder.BLOCKERS)}
    if status != 2 or summary != expected:
        raise ValueError('not a successful blocked build record')
    if (record['schema'] != 'rescue-mac-resident-library-build/1'
            or any(record[key] != 'blocked' for key in ('status', 'activation', 'distribution'))
            or record['sourceCommit'] != revision or record['sourceTree'] != tree
            or record['settings'] != {'GOVERSION': 'go1.27.3', 'GOOS': 'darwin', 'GOARCH': arch, 'CGO_ENABLED': '1'}
            or record['producerExecutable'] is not False or record['cryptographicAttestation'] is not False
            or record['archive']['path'] != 'residentpipe-darwin-' + arch + '.a'
            or record['blockers'] != list(builder.BLOCKERS)):
        raise ValueError('source build record changed or activated')


def run():
    arch = builder.ARCHES.get(platform.machine())
    if len(sys.argv) != 2 or sys.platform != 'darwin' or arch is None:
        raise ValueError('unsupported source build verification')
    output = Path(sys.argv[1])
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'GOMODCACHE', 'DEVELOPER_DIR')}
    result = completed([sys.executable, str(builder.ROOT / 'scripts/build_mac_resident_source.py'),
                        '--output', str(output)], env, builder.ROOT)
    if result.returncode != 2:
        raise ValueError('source builder did not record a blocked archive')
    summary = json.loads(result.stdout)
    record = json.loads((output / 'BUILD_RECORD.json').read_text())
    revision, tree = subprocess.check_output(['git', '-C', str(builder.ROOT), 'rev-parse', 'HEAD', 'HEAD^{tree}'], text=True).splitlines()
    validate_result(result.returncode, summary, record, revision, tree, arch)
    archive = output / ('residentpipe-darwin-' + arch + '.a')
    if hashlib.sha256(archive.read_bytes()).hexdigest() != record['archive']['sha256']:
        raise ValueError('recorded source archive mismatch')
    print(json.dumps({'fixture': 'resident-library-source-ci', 'status': 'blocked',
                      'activation': 'blocked', 'distribution': 'blocked', 'sourceCommit': revision,
                      'sourceTree': tree, 'architecture': arch, 'builderExit': 2,
                      'producerExecutable': False, 'cryptographicAttestation': False}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
        print('MAC_LIBRARY_SOURCE_CI_FAILED', file=sys.stderr)
        raise SystemExit(1)
