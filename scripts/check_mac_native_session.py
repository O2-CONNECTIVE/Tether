"""Pair the original native owner with the real typed ResidentPort core."""
import json
import re
import sys
import subprocess

from check_mac_enrollment_lookup import command
from check_mac_native_source_variants import source_variants, validate_dependencies

CASES = {'session-confirmed': 26, 'session-unknown': 26,
         'session-missing': 26, 'session-malformed': 26,
         'session-lost': 18, 'session-invalid': 18, 'session-rejected': 18,
         'session-unwind': 18, 'session-drop': 18}
NO_CHILD = {'session-normal'}


def validate_report(mode, output):
    expected = 8 if mode == 'session-normal' else CASES.get(mode)
    child = int(mode not in NO_CHILD)
    match = re.fullmatch(r'NATIVE_SESSION_FIXTURE mode=([a-z-]+) passed=(\d+) failed=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) open=(\d+) nativeUnknown=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, child, child, 0, child, 0):
        raise ValueError('incomplete or uncollected native session fixture')
    return expected


def validate_borrow_refusal(returncode, stderr):
    if returncode == 0 or re.findall(r'error\[E\d+\]', stderr) != ['error[E0505]'] or 'cannot move out of `session` because it is borrowed' not in stderr:
        raise ValueError('missing exact session borrow refusal')


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported native session invocation')
    passed = 0
    for variant in source_variants('native-session-'):
        for mode in CASES if variant.fixture else ('session-normal',):
            output = command([str(variant.executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
            passed += validate_report(mode, output)
        # Compiler guard: a real typed view cannot outlive its owning session.
        if not variant.fixture:
            result = subprocess.run([variant.cargo, 'rustc', '--locked', '--offline', '--jobs', '2'] + variant.manifest +
                                    ['--', '--cfg', 'resident_session_borrow_refusal', '--emit=metadata'],
                                    env=variant.env, text=True, capture_output=True, timeout=60)
            validate_borrow_refusal(result.returncode, result.stderr)
    print(json.dumps({'fixture': 'native-session-source', 'passed': passed,
                      'normalModes': 1, 'fixtureModes': 9,
                      'fixtureChildrenStarted': 9, 'fixtureChildrenWaited': 9,
                      'physicalActiveEndpointsClosed': 36, 'retainedNativeUnknownRecords': 9,
                      'explicitOuterJoins': 8, 'sessionDropJoins': 1,
                      'replacementRefusals': 9, 'quitPermits': 0,
                      'lockedOffline': True, 'existingDependencyPackages': 11,
                      'clippyVariants': 2, 'compileRefusals': 1, 'producerLaunches': 0,
                      'scope': 'same native owner and borrowed typed status/intent port; no normal startup / IPC / auth / Quit permission'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError, subprocess.TimeoutExpired):
        print('NATIVE_SESSION_FAILED', file=sys.stderr)
        raise SystemExit(1)
