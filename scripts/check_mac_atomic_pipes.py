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

from check_mac_enrollment_lookup import completed
from check_mac_launch_requirement import NATIVE, validate_weak_link

# passed, allocations, closed, spawned (each child and its thread must be joined)
CASES = {'allocation': (11, 2, 4, 0), 'partial': (21, 3, 6, 0),
         'lost': (21, 3, 6, 0), 'concurrent': (65, 16, 32, 16), 'guards': (9, 0, 0, 0)}
REPORT = r'NATIVE_ATOMIC_PIPE mode=([a-z]+) passed=(\d+) failed=(\d+) allocations=(\d+) closed=(\d+) open=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) threadsJoined=(\d+) producer=(\d+)\n?'


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


def legacy_mutant(source):
    """Restore the old pipe + later fcntl algorithm in a temporary copy only."""
    validate_source(source)
    changes = [
        ('Darwin.pipe2(&pair, O_CLOEXEC)', 'Darwin.pipe(&pair)'),
        (' && fcntl($0, F_GETFD) & FD_CLOEXEC != 0', ''),
        ('        // Parent write/read only.', '''        for end in ends {
            let flags = fcntl(end.fd, F_GETFD)
            guard flags >= 0, fcntl(end.fd, F_SETFD, flags | FD_CLOEXEC) != -1 else {
                throw PreparationFailure.ioPreparation
            }
        }
        // Parent write/read only.'''),
    ]
    for old, new in changes:
        if source.count(old) != 1:
            raise ValueError('legacy mutation no longer matches the reviewed source')
        source = source.replace(old, new)
    return source


def report_counts(mode, output):
    match = re.fullmatch(REPORT, output)
    if mode not in CASES or not match or match.group(1) != mode:
        raise ValueError('missing atomic pipe result')
    return tuple(map(int, match.groups()[1:]))


def validate_report(mode, output):
    counts = report_counts(mode, output)
    passed, allocations, closed, children = CASES[mode]
    if counts != (passed, 0, allocations, closed, 0, children, children, children, 0):
        raise ValueError('incomplete, failed or uncollected atomic pipe fixture')
    return passed


def command(arguments, env, progress):
    progress['returncode'] = None
    result = completed(arguments, env)
    progress['returncode'] = result.returncode
    if result.returncode:
        raise ValueError('native pipe command failed')
    return result.stdout


def execute_mode(executable, mode, progress, mutant=False):
    if mode not in CASES or (mutant and mode != 'concurrent'):
        raise ValueError('unsupported native pipe mode')
    label = 'mutant' if mutant else 'fixture'
    progress.update(phase=label + '-execute', mode=mode, returncode=None, condition=None, child_exit=None)
    result = completed([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
    progress['returncode'] = result.returncode
    # Only finite, known diagnostic fields are retained. No raw stderr, path,
    # descriptor/inode values, compiler arguments or arbitrary error text.
    for field, marker, limit in [('condition', 'CONDITION', CASES[mode][0]), ('child_exit', 'CHILD_EXIT', 255)]:
        values = re.findall(r'(?m)^NATIVE_ATOMIC_PIPE_' + marker + r' ([0-9]{1,3})$', result.stderr)
        if len(values) == 1 and 1 <= int(values[0]) <= limit:
            progress[field] = int(values[0])
    if result.returncode != (1 if mutant else 0):
        raise ValueError('native pipe fixture exited unexpectedly')
    progress['phase'] = label + '-report'
    if mutant:
        # Every child exits specifically for descriptor inheritance, is waited,
        # and has its pthread joined. All owned endpoints are closed afterwards.
        if report_counts(mode, result.stdout) != (33, 32, 16, 32, 0, 16, 16, 16, 0) or \
                progress['condition'] != 1 or progress['child_exit'] != 89:
            raise ValueError('legacy mutation did not fail at the expected executed boundary')
        return 32
    return validate_report(mode, result.stdout)


def run(progress=None):
    if progress is None:
        progress = {}
    progress.update(phase='invocation', mode=None, returncode=None, condition=None, child_exit=None)
    if len(sys.argv) != 1 or sys.platform != 'darwin' or platform.machine() != 'arm64':
        raise ValueError('requires an Apple Silicon Mac fixture host')
    progress['phase'] = 'runtime'
    require_version(platform.mac_ver()[0])
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR')}
    progress['phase'] = 'sdk'
    require_version(command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-version'], env, progress).strip())
    progress['phase'] = 'toolchain'
    swift = command(['/usr/bin/xcrun', '--find', 'swiftc'], env, progress).strip()
    sdk = command(['/usr/bin/xcrun', '--sdk', 'macosx', '--show-sdk-path'], env, progress).strip()
    if not Path(swift).is_file() or not Path(sdk).is_dir():
        raise ValueError('existing native compiler unavailable')
    progress['phase'] = 'source'
    owner_source = (NATIVE / 'OwnedPreparationIO.swift').read_text(encoding='utf-8')
    validate_source(owner_source)
    with tempfile.TemporaryDirectory(prefix='resident-atomic-pipes-') as temporary:
        directory = Path(temporary)
        sources = [str(NATIVE / name) for name in ('ResidentLaunchPreparation.swift', 'OwnedPreparationIO.swift', 'PreparedByteIO.swift')]
        # Lower fixture deployment target keeps availability checking live;
        # it does not declare app/atomic IO support for an older runtime.
        base = [swift, '-swift-version', '6', '-warnings-as-errors', '-sdk', sdk,
                '-target', 'arm64-apple-macosx11.0', '-parse-as-library',
                '-module-cache-path', str(directory / 'cache')]
        progress.update(phase='normal-build', returncode=None)
        command(base + ['-whole-module-optimization', '-emit-object'] + sources + ['-o', str(directory / 'normal.o')], env, progress)
        link = [str(NATIVE / 'fixtures/AtomicPipeDriver.swift'), '-Xlinker', '-weak_framework', '-Xlinker', 'LightweightCodeRequirements']
        mutant_source = directory / 'OwnedPreparationIO.swift'
        mutant_source.write_text(legacy_mutant(owner_source), encoding='utf-8')
        mutant_executable = directory / 'legacy-pipe-fixture'
        progress.update(phase='mutant-build', mode=None, returncode=None)
        command(base + ['-D', 'RESIDENT_LAUNCH_FIXTURE'] + [sources[0], str(mutant_source), sources[2]] + link + ['-o', str(mutant_executable)], env, progress)
        rejected = execute_mode(mutant_executable, 'concurrent', progress, mutant=True)
        executable = directory / 'atomic-pipe-fixture'
        progress.update(phase='fixture-build', mode=None, returncode=None, condition=None, child_exit=None)
        command(base + ['-D', 'RESIDENT_LAUNCH_FIXTURE'] + sources + link + ['-o', str(executable)], env, progress)
        progress['phase'] = 'weak-link'
        validate_weak_link(command(['/usr/bin/otool', '-l', str(executable)], env, progress))
        passed = sum(execute_mode(executable, mode, progress) for mode in CASES)
    print(json.dumps({'fixture': 'atomic-owned-preparation-pipes', 'passed': passed,
                      'allocations': 24, 'ownedEndsClosed': 48, 'fixtureChildrenStarted': 16,
                      'fixtureChildrenWaited': 16, 'fixtureThreadJoins': 16, 'producerLaunches': 0,
                      'runtime': platform.mac_ver()[0], 'normalCompileTargets': 1, 'fixtureCompileTargets': 1,
                      'executedLegacyMutation': {'failedConditions': rejected, 'childrenWaited': 16, 'threadJoins': 16, 'ownedEndsClosed': 32},
                      'scope': 'atomic allocation and independent self-child exec only; no signed launch or product activation'}))


if __name__ == '__main__':
    progress = {}
    try:
        run(progress)
    except (OSError, ValueError, KeyError, IndexError):
        print('NATIVE_ATOMIC_PIPE_FIXTURE_FAILED ' + json.dumps(progress, sort_keys=True), file=sys.stderr)
        raise SystemExit(1)
