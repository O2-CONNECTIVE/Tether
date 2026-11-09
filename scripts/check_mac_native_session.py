"""Pair the original native owner with the real typed ResidentPort core."""
import json
import re
import sys
import subprocess

from check_mac_enrollment_lookup import command, completed
from check_mac_native_source_variants import source_variants, validate_dependencies

CASES = {'session-confirmed': 26, 'session-unknown': 26,
         'session-missing': 26, 'session-malformed': 26,
         'session-lost': 18, 'session-invalid': 18, 'session-rejected': 18,
         'session-unwind': 18, 'session-drop': 18,
         'session-close-pending': 28, 'session-close-idle': 21}
NO_CHILD = {'session-normal'}
TERMINAL = {'session-confirmed', 'session-rejected', 'session-close-idle', 'session-close-pending', 'session-drop'}


def validate_report(mode, output):
    expected = 8 if mode == 'session-normal' else CASES.get(mode)
    child = int(mode not in NO_CHILD)
    unknown = int(mode not in NO_CHILD and mode not in TERMINAL)
    match = re.fullmatch(r'NATIVE_SESSION_FIXTURE mode=([a-z-]+) passed=(\d+) failed=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) open=(\d+) nativeUnknown=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, child, child, 0, unknown, 0):
        raise ValueError('incomplete or uncollected native session fixture')
    return expected


def validate_borrow_refusal(returncode, stderr):
    if returncode == 0 or re.findall(r'error\[E\d+\]', stderr) != ['error[E0505]'] or 'cannot move out of `session` because it is borrowed' not in stderr:
        raise ValueError('missing exact session borrow refusal')


def execute_mode(executable, mode, progress):
    # The executable is a fixed source fixture, not a producer. Never publish
    # raw stderr, paths, exception text or payloads from the child.
    progress.update(phase='fixture', mode=mode, returncode=None, condition=None)
    result = completed([str(executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
    progress['returncode'] = result.returncode
    if result.returncode:
        match = re.search(r'\nsession condition ([1-9][0-9]?)\n', result.stderr)
        if match and 1 <= int(match.group(1)) <= 26:
            progress['condition'] = int(match.group(1))
        raise ValueError('native session fixture failed')
    progress['phase'] = 'report'
    return validate_report(mode, result.stdout)


def run(progress=None):
    if progress is None:
        progress = {}
    progress.update(phase='invocation', mode=None, returncode=None, condition=None)
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported native session invocation')
    passed = 0
    progress['phase'] = 'normal-build'
    for variant in source_variants('native-session-'):
        for mode in CASES if variant.fixture else ('session-normal',):
            passed += execute_mode(variant.executable, mode, progress)
        # Compiler guard: a real typed view cannot outlive its owning session.
        if not variant.fixture:
            progress.update(phase='borrow-refusal', mode=None, returncode=None, condition=None)
            result = subprocess.run([variant.cargo, 'rustc', '--locked', '--offline', '--jobs', '2'] + variant.manifest +
                                    ['--', '--cfg', 'resident_session_borrow_refusal', '--emit=metadata'],
                                    env=variant.env, text=True, capture_output=True, timeout=60)
            progress['returncode'] = result.returncode
            validate_borrow_refusal(result.returncode, result.stderr)
        progress.update(phase='fixture-build', mode=None, returncode=None, condition=None)
    print(json.dumps({'fixture': 'native-session-source', 'passed': passed,
                      'normalModes': 1, 'fixtureModes': 11,
                      'fixtureChildrenStarted': 11, 'fixtureChildrenWaited': 11,
                      'physicalActiveEndpointsClosed': 44, 'retainedNativeUnknownRecords': 6, 'retainedTerminalRecords': 5,
                      'explicitOuterJoins': 10, 'sessionDropJoins': 1,
                      'replacementRefusals': 11, 'quitPermits': 4,
                      'lockedOffline': True, 'existingDependencyPackages': 11,
                      'clippyVariants': 2, 'compileRefusals': 1, 'producerLaunches': 0,
                      'scope': 'same native owner and borrowed typed status/intent port; fixture terminal Quit proof only; no normal startup / IPC / auth'}))


if __name__ == '__main__':
    progress = {}
    try:
        run(progress)
    except (OSError, ValueError, KeyError, subprocess.TimeoutExpired):
        print('NATIVE_SESSION_FAILED ' + json.dumps(progress, sort_keys=True), file=sys.stderr)
        raise SystemExit(1)
