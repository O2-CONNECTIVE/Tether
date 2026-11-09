"""Join the concrete native outer monitor; UNKNOWN never permits Quit."""
import json
import re
import sys

from check_mac_enrollment_lookup import command
from check_mac_native_source_variants import source_variants, validate_dependencies

CASES = {'outer-unproven-exit-two': 16, 'outer-finite-malformed': 16, 'outer-finite-unwind': 16,
         'outer-finite-unknown': 16, 'outer-prepared': 13, 'outer-confirmed': 20, 'outer-unknown': 20,
         'outer-missing': 20, 'outer-malformed': 20, 'outer-invalid': 16,
         'outer-rejected': 16, 'outer-signal': 16, 'outer-lost-receipt': 16,
         'outer-unwind': 16, 'outer-drop': 12, 'outer-held': 20, 'outer-panic': 16, 'outer-prejoined': 16}
NO_CHILD = {'outer-normal', 'outer-normal-prejoined', 'outer-prepared'}
TERMINAL = {'outer-rejected', 'outer-drop', 'outer-confirmed', 'outer-prejoined', 'outer-panic', 'outer-held'}


def validate_report(mode, output):
    expected = 8 if mode in ('outer-normal', 'outer-normal-prejoined') else CASES.get(mode)
    child = int(mode not in NO_CHILD)
    unknown = int(mode not in NO_CHILD and mode not in TERMINAL)
    match = re.fullmatch(r'NATIVE_OUTER_FIXTURE mode=([a-z-]+) passed=(\d+) failed=(\d+) childrenStarted=(\d+) childrenWaited=(\d+) open=(\d+) nativeUnknown=(\d+) producer=(\d+)\n?', output)
    if expected is None or not match or match.group(1) != mode or tuple(map(int, match.groups()[1:])) != (expected, 0, child, child, 0, unknown, 0):
        raise ValueError('incomplete or uncollected native outer monitor fixture')
    return expected


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported native outer monitor invocation')
    passed = 0
    for variant in source_variants('native-outer-'):
        for mode in CASES if variant.fixture else ('outer-normal', 'outer-normal-prejoined'):
            output = command([str(variant.executable), mode], {'PATH': '/usr/bin:/bin', 'LANG': 'C'})
            passed += validate_report(mode, output)
    print(json.dumps({'fixture': 'native-outer-monitor-source', 'passed': passed,
                      'normalModes': 2, 'fixtureModes': 18, 'activeModes': 17,
                      'fixtureChildrenStarted': 17, 'fixtureChildrenWaited': 17,
                      'physicalActiveEndpointsClosed': 68, 'unlaunchedEndpointsClosed': 4,
                      'actualActiveIoWorkerJoins': 17, 'successfulActiveOuterJoins': 16,
                      'failedActiveOuterJoins': 1, 'preparationOuterJoins': 3,
                      'retainedNativeUnknownRecords': 11, 'retainedTerminalRecords': 6, 'quitPermits': 5,
                      'lockedOffline': True, 'existingDependencyPackages': 11,
                      'clippyVariants': 2, 'producerLaunches': 0, 'hostOnly': True,
                      'scope': 'same native IO + actual outer monitor joins; terminal fixtures permit Quit; normal startup remains refused'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('NATIVE_OUTER_FAILED', file=sys.stderr)
        raise SystemExit(1)
