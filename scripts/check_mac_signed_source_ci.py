"""Compile release inputs with synthetic identity; never sign/load/launch product code."""
import json
import os
from pathlib import Path
import platform
import re
import sys
import tempfile
from unittest.mock import patch

import mac_signed_release_support as release
from check_mac_enrollment_lookup import completed

ROOT = Path(__file__).resolve().parents[1]


def command(arguments, env, cwd=None):
    result = completed(arguments, env, cwd)
    if result.returncode:
        # This CI-only driver compiles checked-in or anonymous generated source.
        # Show bounded compiler error headlines, never source lines or raw paths.
        headlines = []
        for line in result.stderr.splitlines():
            match = re.search(r'(?:error|warning): (.*)', line)
            if match:
                headlines.append(re.sub(r'(?:[A-Za-z]:)?/[^\s:]+', '<path>', match.group(1))[:300])
        for line in headlines[:12]:
            print('SIGNED_SOURCE_COMPILER_DIAGNOSTIC=' + line, file=sys.stderr)
        raise ValueError('fixture command failed')
    return result.stdout


def phase(name):
    print('SIGNED_SOURCE_PHASE=' + name, file=sys.stderr)


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin' or platform.machine() != 'arm64' or \
            int(platform.mac_ver()[0].split('.')[0]) < 27 or os.environ.get('RUNNER_ENVIRONMENT') != 'github-hosted':
        raise ValueError('disposable macOS 27 ARM64 required')
    env = {key: value for key, value in os.environ.items() if key in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR')}
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    native = ROOT / release.support.NATIVE_ROOT
    with tempfile.TemporaryDirectory(prefix='signed-source-fixture-') as temporary:
        stage = Path(temporary).resolve()
        observer_source = stage / release.OBSERVER
        observer_source.parent.mkdir()
        observer_source.write_bytes((ROOT / release.OBSERVER).read_bytes())
        phase('observer-compile')
        with patch.object(release.source, 'command',
                          lambda args, cwd, env: command(args, env, cwd).encode()):
            observer = release.compile_observer(stage, env, swift, sdk)
        # Anonymous compile fixture only. Not a product build or approval record.
        provider = stage / release.PROVIDER
        provider.parent.mkdir()
        provider.write_bytes(release.provider_bytes({'teamIdentifier': 'FIXTURE123', 'cdhash': 'd' * 40}))
        sources = [str(native / name) for name in release.support.SWIFT_FILES]
        base = [swift, '-swift-version', '6', '-warnings-as-errors', '-sdk', sdk,
                '-target', 'arm64-apple-macosx27.0', '-parse-as-library',
                '-module-cache-path', str(stage / 'swift-cache'),
                '-import-objc-header', str(native / 'ResidentPreparation.h')]
        # Ordinary production still compiles without the generated input.
        phase('normal-typecheck')
        command(base + ['-typecheck'] + sources, env)
        signed = base + ['-D', 'TETHER_SIGNED_RELEASE']
        output = stage / 'libReleaseSourceFixture.dylib'
        phase('generated-provider-compile')
        command(signed + ['-emit-library', '-no-toolchain-stdlib-rpath'] + sources + [str(provider),
                          '-Xlinker', '-weak_framework', '-Xlinker', 'LightweightCodeRequirements',
                          '-o', str(output)], env)
        phase('abi-exports')
        symbols = command(['/usr/bin/nm', '-gUj', str(output)], env).splitlines()
        if {line.strip() for line in symbols if line.startswith('_rh_')} != release.support.EXPORTS:
            raise ValueError('release variant changed the C ABI')
        phase('provider-compile-refusals')
        missing = completed(signed + ['-typecheck'] + sources, env)
        duplicate = stage / 'DuplicateIdentity.swift'
        duplicate.write_bytes(provider.read_bytes())
        repeated = completed(signed + ['-typecheck'] + sources + [str(provider), str(duplicate)], env)
        if missing.returncode == 0 or 'SignedResidentReleaseIdentity' not in missing.stderr or \
                repeated.returncode == 0 or 'redeclaration' not in repeated.stderr:
            raise ValueError('missing or duplicate provider did not fail compilation')
        # The compiler's ordinary ad-hoc output must never become Developer ID.
        untrusted_source = stage / 'untrusted.swift'
        untrusted_source.write_text('print("unused fixture")\n')
        untrusted = stage / 'resident-pipe'
        phase('adhoc-fixture-compile')
        command([swift, '-sdk', sdk, '-target', 'arm64-apple-macosx27.0', str(untrusted_source), '-o', str(untrusted)], env)
        request = [str(observer), '--path', str(untrusted), '--team-id', 'FIXTURE123',
                   '--identifier', release.IDENTIFIERS['producer'], '--certificate-sha1', 'C' * 40]
        phase('adhoc-refusal')
        refused = completed(request, env)
        if refused.returncode != 2 or refused.stdout or \
                refused.stderr.strip() != 'mac-release-signature: signature-or-identity-invalid':
            raise ValueError('untrusted compiler output was not rejected')
        phase('argument-refusal')
        malformed = completed(request + ['--unknown', 'PRIVATE'], env)
        if malformed.returncode != 2 or malformed.stdout or 'PRIVATE' in malformed.stderr:
            raise ValueError('unexpected helper arguments were not rejected')
    print(json.dumps({'fixture': 'mac-signed-release-source', 'normalTypecheck': True,
                      'syntheticReleaseCompiled': True, 'providerCompileRefusals': 2,
                      'adHocRefusals': 1, 'argumentRefusals': 1, 'signingPerformed': False,
                      'productExecuted': False, 'productLoaded': False,
                      'scope': 'compiler and unsigned refusal only; genuine Developer ID unverified'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError, release.source.BuildError):
        print('SIGNED_RELEASE_SOURCE_FIXTURE_FAILED', file=sys.stderr)
        raise SystemExit(1)
