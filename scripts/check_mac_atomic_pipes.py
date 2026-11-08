"""Verify atomic Darwin pipe acquisition with fixed, untrusted self-children.

Requires an existing macOS 27 runtime and SDK. No tool installation, producer,
GUI, signing, policy, credentials, or external connection.
"""
import json
import os
from pathlib import Path
import platform
import re
import sys
import tempfile

from check_mac_enrollment_lookup import command
from check_mac_launch_requirement import NATIVE, validate_weak_link

# passed, allocations, closed, spawned (every child must also be waited)
CASES = {'allocation': (11, 2, 4, 0), 'partial': (21, 3, 6, 0),
         'lost': (21, 3, 6, 0), 'concurrent': (65, 16, 32, 16), 'guards': (9, 0, 0, 0)}


def require_version(value):
    if not re.fullmatch(r'\d+\.\d+(?:\.\d+)?', value) or int(value.split('.')[0]) < 27:
        raise ValueError('atomic pipe fixture requires macOS runtime and SDK 27 or later')


def validate_source(source):
    """Narrow regression guard, not a substitute for SDK compile / real exec."""
    if len(re.findall(r'Darwin\.pipe2\(&pair, O_CLOEXEC\)', source)) != 1:
        raise ValueError('missing direct atomic allocation')
    if re.search(r'\bpipe\s*\(|\bF_SETFD\b|\bdlsym\b|@_silgen_name', source):
        raise ValueError('non-atomic fallback or untyped API substitute')
    configure = source.split('    func configure(', 1)[1].split('    @available(macOS 27.0, *)', 1)[0]
    if not configure.index('#available(macOS 27.0, *)') < configure.index('try adoptPipe()'):
        raise ValueError('runtime gate must precede allocation')
    adopt = source.split('    private func adoptPipe()', 1)[1].split('    private func nonblocking', 1)[0]
    if not adopt.index('Darwin.pipe2') < adopt.index('ends.append') < adopt.index('try fixtureAllocationProbe?') < adopt.index('guard pair.allSatisfy'):
        raise ValueError('acquisition must be owned and observed before post-allocation verification')


def validate_report(mode, output):
    expected = CASES.get(mode)
    match = re.fullmatch(r'NATIVE_ATOMIC_PIPE mode=([a-z]+) passed=(\d+) failed=(\d+) allocations=(\d+) closed=(\d+) open=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode:
        raise ValueError('missing atomic pipe result')
    passed, allocations, closed, children = expected
    if tuple(map(int, match.groups()[1:])) != (passed, 0, allocations, closed, 0, children, children, 0):
        raise ValueError('incomplete, failed or uncollected atomic pipe fixture')
    return passed


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin' or platform.machine() != 'arm64':
        raise ValueError('requires an Apple Silicon Mac fixture host')
    require_version(platform.mac_ver()[0])
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR')}
    require_version(command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-version'], env).strip())
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env).strip()
    if not Path(swift).is_file() or not Path(sdk).is_dir():
        raise ValueError('existing native compiler unavailable')
    validate_source((NATIVE / 'OwnedPreparationIO.swift').read_text(encoding='utf-8'))
    with tempfile.TemporaryDirectory(prefix='resident-atomic-pipes-') as temporary:
        directory = Path(temporary)
        sources = [str(NATIVE / name) for name in ('ResidentLaunchPreparation.swift', 'OwnedPreparationIO.swift', 'PreparedByteIO.swift')]
        # The lower fixture deployment target keeps availability checking live.
        # It does not declare that the app or atomic IO supports an older OS.
        base = [swift, '-swift-version', '6', '-warnings-as-errors', '-sdk', sdk,
                '-target', 'arm64-apple-macosx11.0', '-parse-as-library',
                '-module-cache-path', str(directory / 'cache')]
        command(base + ['-whole-module-optimization', '-emit-object'] + sources + ['-o', str(directory / 'normal.o')], env)
        executable = directory / 'atomic-pipe-fixture'
        command(base + ['-D', 'RESIDENT_LAUNCH_FIXTURE'] + sources + [str(NATIVE / 'fixtures/AtomicPipeDriver.swift'),
                        '-Xlinker', '-weak_framework', '-Xlinker', 'LightweightCodeRequirements', '-o', str(executable)], env)
        validate_weak_link(command(['/usr/bin/otool', '-l', str(executable)], env))
        passed = sum(validate_report(mode, command([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})) for mode in CASES)
    print(json.dumps({'fixture': 'atomic-owned-preparation-pipes', 'passed': passed,
                      'allocations': 24, 'ownedEndsClosed': 48, 'fixtureChildrenStarted': 16,
                      'fixtureChildrenWaited': 16, 'producerLaunches': 0,
                      'runtime': platform.mac_ver()[0], 'normalCompileTargets': 1, 'fixtureCompileTargets': 1,
                      'scope': 'atomic allocation and independent self-child exec only; no signed launch or product activation'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError, IndexError):
        print('NATIVE_ATOMIC_PIPE_FIXTURE_FAILED', file=sys.stderr)
        raise SystemExit(1)
