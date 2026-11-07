"""Fail-closed empirical reuse gate; no process mutation or descendant-closure claim."""
from __future__ import annotations
import ntpath

TRUE_FIELDS = ('normalCloseVerified', 'appCloseObservedBeforeCleanup',
               'closeObservedBeforeCleanup', 'driverStableThroughClose',
               'driverExitObservedAfterCleanup', 'cleanupStarted', 'appStartupFileClosed')
BASENAMES = {'rescue-host-shell.exe', 'tauri-driver.exe', 'msedgedriver.exe'}
SNAPSHOT_KEYS = {'snapshotComplete', 'matchedExecutablePathsAbsent', 'portsVacant',
                 'candidateCount', 'matchedPathCount', 'eligible'}


def integer(value, low, high):
    return type(value) is int and low <= value <= high


def windows_path(value):
    if type(value) is not str or not value or len(value) > 32767 or any(ord(c) < 32 for c in value):
        return None
    value = value.replace('/', '\\')
    drive, tail = ntpath.splitdrive(value)
    if not drive or not tail.startswith('\\') or any(p in ('.', '..') for p in tail.split('\\')):
        return None
    return ntpath.normcase(ntpath.normpath(value))


def snapshot_summary(rows, listeners, expected_paths, process_ok=True, listener_ok=True):
    """Consume transient Name/path rows and integer TCP listener ports, publish scalars only.

    This query covers named candidates and exact paths, not all descendants. Missing
    paths for unrelated named system processes do not invalidate the scoped query.
    """
    complete = process_ok is True
    expected = ([windows_path(p) for p in expected_paths]
                if type(expected_paths) is list and len(expected_paths) == 3 else [])
    if (len(expected) != 3 or None in expected or len(set(expected)) != 3
            or {ntpath.basename(p) for p in expected if p} != BASENAMES):
        complete = False
    candidates = matched = 0
    if type(rows) is not list or len(rows) > 4096:
        complete = False
    else:
        for row in rows:
            if type(row) is not dict:
                complete = False
                continue
            name, raw_path = row.get('Name'), row.get('ExecutablePath')
            if (type(name) is not str or not name or len(name) > 256
                    or name.strip() != name or not name.strip()
                    or any(ord(c) < 32 for c in name) or any(c in name for c in ('\\', '/'))):
                complete = False
                continue
            path = windows_path(raw_path)
            named = name.casefold() in BASENAMES
            exact = path is not None and path in expected
            if named or exact:
                candidates += 1
                if path is None:
                    complete = False
                if exact:
                    matched += 1
            elif raw_path not in (None, '') and path is None:
                complete = False
    vacant = (listener_ok is True and type(listeners) is list and len(listeners) <= 4096
              and all(integer(port, 1, 65535) for port in listeners)
              and not any(port in (4444, 4445) for port in listeners))
    return {'snapshotComplete': complete, 'matchedExecutablePathsAbsent': complete and matched == 0,
            'portsVacant': vacant, 'candidateCount': candidates, 'matchedPathCount': matched,
            'eligible': complete and matched == 0 and vacant}


def child_count(facts, require_exited=True):
    if type(facts) is not dict or not integer(facts.get('observed'), 1, 32):
        return None
    count = facts['observed']
    if any(not integer(facts.get(key), 0, 0) for key in ('remainingSnapshot', 'omittedRows')):
        return None
    rows = facts.get('rows')
    if type(rows) is not list or len(rows) != count:
        return None
    slots = []
    for row in rows:
        if (type(row) is not dict or not integer(row.get('slot'), 1, count)
                or type(row.get('handleIdentityVerified')) is not bool
                or row.get('handleState') not in ('live', 'exited', 'unavailable', 'different-identity')
                or row.get('snapshotState') not in ('same-identity', 'pid-reused', 'ambiguous', 'absent')):
            return None
        if require_exited and (row['handleIdentityVerified'] is not True
                               or row['handleState'] != 'exited' or row['snapshotState'] != 'absent'):
            return None
        slots.append(row['slot'])
    return count if sorted(slots) == list(range(1, count + 1)) else None


def eligibility(native, child_exit, snapshot_summary):
    """Require both complete raw child sets plus the fresh post-pwsh snapshot."""
    native = native if type(native) is dict else {}
    healthy = (integer(child_exit, 0, 0)
               and all(native.get(key) is True for key in TRUE_FIELDS)
               and all(integer(native.get(key), 1, 1) for key in ('nativeTestsRun', 'nativeTestsPassed'))
               and all(integer(native.get(key), 0, 0) for key in
                       ('appExitCode', 'cleanupFailures', 'childHandleDisposeFailures')))
    # The earlier observation binds the complete recorded slot set, not exit timing.
    before = child_count(native.get('childCloseFacts'), require_exited=False)
    after = child_count(native.get('afterDriverCleanupFacts'))
    children = before is not None and after == before
    snapshot = snapshot_summary
    valid = (type(snapshot) is dict and set(snapshot) == SNAPSHOT_KEYS
             and all(type(snapshot[k]) is bool for k in
                     ('snapshotComplete', 'matchedExecutablePathsAbsent', 'portsVacant', 'eligible'))
             and integer(snapshot['candidateCount'], 0, 4096)
             and integer(snapshot['matchedPathCount'], 0, snapshot['candidateCount'])
             and snapshot['matchedExecutablePathsAbsent'] ==
                 (snapshot['snapshotComplete'] and snapshot['matchedPathCount'] == 0)
             and snapshot['eligible'] == (snapshot['snapshotComplete']
                 and snapshot['matchedExecutablePathsAbsent'] and snapshot['portsVacant']))
    ready = valid and all(snapshot[k] for k in
                         ('snapshotComplete', 'matchedExecutablePathsAbsent', 'portsVacant'))
    return {'originalVerdictHealthy': healthy, 'recordedChildrenExitVerified': children,
            'recordedChildrenCount': after if children else 0,
            'snapshotComplete': valid and snapshot['snapshotComplete'],
            'matchedExecutablePathsAbsent': valid and snapshot['matchedExecutablePathsAbsent'],
            'portsVacant': valid and snapshot['portsVacant'],
            'eligible': healthy and children and ready}
