"""Create a blocked standby-only local archive; never render, install or run it."""
import io
import json
import os
from pathlib import Path
import re
import stat
import zipfile

import package_mac_agent as base

ROOT = Path(__file__).resolve().parent.parent
TEMPLATE_MEMBER = 'templates/local.tether.host.standby.evaluation.plist.in'
TEMPLATE_PATH = ROOT / 'packaging/macos/local.tether.host.standby.evaluation.plist.in'
TEMPLATE_SHA256 = '7e6895947a976ea85fa0e5adc4363c1c2a345346259fcd3cc5f9d67ae8b99921'
# Fixed reviewed texts, not a claim about this binary's linked dependency closure.
TEXTS = {name: base.TEXTS[name] for name in (
    'go-toolchain/go1.27.3/LICENSE', 'go-toolchain/go1.27.3/PATENTS',
    'sys-v0.49.0/LICENSE', 'sys-v0.49.0/PATENTS')}
INSTRUCTIONS = """Tether Host standby local evaluation candidate — DISTRIBUTION BLOCKED

This unsigned archive is NOT an installer or an authenticated publisher release.
No binary is executed and no service, login item or LaunchAgent is registered.
The .plist.in is an INACTIVE TEMPLATE with an unresolved executable placeholder.
Do not load it, move it to LaunchAgents, or treat packaging as permission to run.
No renderer, deployment path selection or installation command is provided.

The intended future entry is the fixed tether-host-standby binary with --standby.
It waits silently with ZERO execution authority; no grant, key, configuration,
network, command worker, viewer, reconnect or approval is supplied. The optional
status stream is not used: a launchd output file is not its dedicated nonblocking
FIFO. Standby output is NOT compatible with the existing Swift viewer protocol.

Aqua in the template expresses an intended post-login restriction, not tested
login-session proof. Root/elevation refusal is not isolation or a sandbox.
Duplicate-instance protection, logout/signal delivery and update/removal are
unverified. Being alive is NOT remote readiness or cleanup of previous work.

Hashes identify bytes, not publisher identity, safety or binary product identity.
The supplied source-manifest hash is only a label; binary-to-source correspondence,
actual linked dependencies, complete notices, embedded secret/build-path audit,
signing, notarization and compatibility are all UNVERIFIED. Fixed Go and sys
license texts are not complete distribution or legal clearance. Never use test
fixture texts as real release evidence. Minimum OS/SDK are header declarations.

Keep this candidate separate from rescue-mac-agent and Tether Status.app. Existing
agent grants and MCP sessions remain one-shot; no authorization can be inherited.
A future fixed current-user placement and explicit login opt-in require a separate
reviewed lifecycle. No automatic install, overwrite, restart or self-update exists.
"""


def package(binary, expected_sha256, target, source_manifest_sha256, notices_root, output):
    base.require(bool(re.fullmatch('[0-9a-f]{64}', expected_sha256)) and
                 bool(re.fullmatch('[0-9a-f]{64}', source_manifest_sha256)), 'HASH_INVALID')
    output = Path(output)
    base.directory_chain(output.parent)
    base.require(not os.path.lexists(output), 'OUTPUT_EXISTS')
    data = base.read_regular(binary, base.BINARY_LIMIT)
    base.require(base.sha(data) == expected_sha256, 'BINARY_HASH_MISMATCH')
    platform = base.macho(data, target)
    template = base.read_regular(TEMPLATE_PATH, base.TEXT_LIMIT)
    # Exact bytes lock the unrendered fixed dictionary and reject duplicate keys,
    # arbitrary executable/argv, extra launch controls and interpolation changes.
    base.require(base.sha(template) == TEMPLATE_SHA256, 'TEMPLATE_CHANGED')
    files = {'bin/tether-host-standby': data, TEMPLATE_MEMBER: template,
             'LICENSE': base.read_regular(ROOT / 'LICENSE', base.TEXT_LIMIT),
             'RECIPIENT.txt': INSTRUCTIONS.encode('utf-8')}
    for name, digest in TEXTS.items():
        text = base.read_regular(Path(notices_root) / name, base.TEXT_LIMIT)
        base.require(base.sha(text) == digest, 'NOTICE_HASH_MISMATCH')
        files['notices/' + name] = text
    manifest = {'schema': 1, 'product': 'tether-host-standby-evaluation', 'target': target,
                'signingStatus': 'unsigned-unverified', 'distributionStatus': 'blocked',
                'binarySourceCorrespondenceVerified': False, 'binaryProductIdentityVerified': False,
                'binaryDependenciesVerified': False, 'noticeCompletenessVerified': False,
                'binarySecretAuditVerified': False, 'notarizationVerified': False,
                'templateRendered': False, 'registrationPerformed': False,
                'sourceManifestSha256Supplied': source_manifest_sha256,
                'platformDeclaration': platform,
                'files': {name: base.sha(value) for name, value in sorted(files.items())}}
    files['MANIFEST.json'] = (json.dumps(manifest, sort_keys=True, indent=2) + '\n').encode()
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, 'w', compression=zipfile.ZIP_STORED) as archive:
        for name, value in sorted(files.items()):
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.create_system = 3
            info.external_attr = (stat.S_IFREG | (0o555 if name == 'bin/tether-host-standby' else 0o444)) << 16
            archive.writestr(info, value)
    payload = buffer.getvalue()
    # Exclusive output only; an incomplete write is never overwritten/removed.
    # Ancestor concurrent replacement by the same user is not sandboxed.
    fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(payload)
    return {'status': 'local_archive_created', 'distributionStatus': 'blocked',
            'archiveSha256': base.sha(payload), 'binarySha256': expected_sha256}


def main(argv=None):
    try:
        parser = base.Parser(description=__doc__)
        for option in ('binary', 'expected-sha256', 'target', 'source-manifest-sha256', 'notices-root', 'output'):
            parser.add_argument('--' + option, required=True)
        result = package(**vars(parser.parse_args(argv)))
    except base.PackageError as error:
        print(json.dumps({'status': 'refused', 'code': str(error)}))
        return 1
    except (OSError, ValueError, TypeError):
        print(json.dumps({'status': 'refused', 'code': 'IO_OR_INPUT_INVALID'}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
