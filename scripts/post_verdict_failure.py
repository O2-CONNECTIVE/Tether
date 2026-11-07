"""Closed post-verdict admission. Trusted dedicated CI job, not authentication.

PowerShell owns exclusive copying and Flush(true). This reader never writes or
changes a verdict. The publisher must independently verify these exact bytes.
"""
import hashlib
import json
import re
import stat
import sys
from pathlib import Path


def require(ok):
    if not ok:
        raise ValueError('POST_VERDICT_REJECTED')


def fields(value, names):
    require(type(value) is dict and set(value) == set(names.split()))


def integer(value, low=0, high=120000):
    require(type(value) is int and low <= value <= high)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode('ascii')


def decode(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result)
            result[key] = value
        return result
    return json.loads(raw.decode('utf-8'), object_pairs_hook=pairs,
                      parse_constant=lambda _: require(False))


def read(path, limit=65536):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and
            not getattr(info, 'st_file_attributes', 0) & 0x400 and 0 < info.st_size <= limit)
    with path.open('rb') as stream:
        raw = stream.read(limit + 1)
    require(len(raw) == info.st_size)
    return raw


def manifest(raw):
    value = decode(raw)
    fields(value, 'protocol run attempt trial sourceCommit sourceTree controlCommit specimenSha256 nonce')
    require(value['protocol'] == 'post-verdict-manifest-1' and type(value['run']) is str
            and re.fullmatch('[1-9][0-9]{0,19}', value['run']) is not None)
    integer(value['attempt'], 1, 1)
    integer(value['trial'], 1, 1)
    for key in ('sourceCommit', 'sourceTree', 'controlCommit', 'specimenSha256', 'nonce'):
        width = 40 if key in ('sourceCommit', 'sourceTree', 'controlCommit') else 64
        require(type(value[key]) is str and re.fullmatch('[0-9a-f]{%d}' % width, value[key]) is not None)
    require(raw == canonical(value))
    return value


def log_facts(value):
    if value is None:
        return
    fields(value, 'schema purpose stdout stderr')
    integer(value['schema'], 1, 1)
    require(value['purpose'] == 'diagnostic-hints-only')
    hints = 'proxyConnectionError sessionNotCreated devtoolsPortMissing profileInUse browserNotReachable browserExited versionMismatch'
    for key in ('stdout', 'stderr'):
        row = value[key]
        require(type(row) is dict)
        if row.get('status') == 'unavailable':
            fields(row, 'status bytesRead truncated hints')
            require(all(row[k] is None for k in ('bytesRead', 'truncated', 'hints')))
        else:
            fields(row, 'status bytesRead truncated hints proxyErrorKinds')
            require(row['status'] == 'read' and type(row['truncated']) is bool)
            integer(row['bytesRead'], 0, 65536)
            fields(row['hints'], hints)
            require(all(type(v) is bool for v in row['hints'].values()))
            require(type(row['proxyErrorKinds']) is list and len(row['proxyErrorKinds']) <= 4)
            require(all(v in ('incomplete-message', 'canceled', 'io', 'unclassified') for v in row['proxyErrorKinds']))


def validate_failure(native, timing):
    fields(native, 'schema status phase elapsedMs documentObservations documentStates errorCode originClass titleClass droppedRequests phases requests lastRequest')
    integer(native['schema'], 1, 1)
    integer(native['elapsedMs'])
    integer(native['documentObservations'], 0, 0)
    integer(native['droppedRequests'], 0, 0)
    require(native['status'] == 'failed' and native['phase'] == 'session-create' and
            native['errorCode'] == 'NATIVE_DRIVER_TIMEOUT' and native['documentStates'] == [] and
            native['originClass'] == native['titleClass'] == 'unobserved')
    require(type(native['phases']) is list and len(native['phases']) == 1)
    phase = native['phases'][0]
    fields(phase, 'phase elapsedMs')
    require(phase['phase'] == 'session-create')
    integer(phase['elapsedMs'], 0, native['elapsedMs'])
    require(type(native['requests']) is list and len(native['requests']) == 2)
    for index, outcome in enumerate(('started', 'timeout')):
        row = native['requests'][index]
        fields(row, 'operation outcome elapsedMs')
        require(row['operation'] == 'create-session' and row['outcome'] == outcome)
        integer(row['elapsedMs'], 0 if index == 0 else 1, native['elapsedMs'])
    require(native['requests'][0]['elapsedMs'] == 0 and native['lastRequest'] == native['requests'][1])
    fields(timing, 'schema purpose settledMs outcome samples')
    integer(timing['schema'], 1, 1)
    integer(timing['settledMs'], 1, native['elapsedMs'])
    require(timing['purpose'] == 'diagnostic-hints-only' and timing['outcome'] == 'timeout')
    require(type(timing['samples']) is list and 1 <= len(timing['samples']) <= 2)
    for index, row in enumerate(timing['samples']):
        fields(row, 'stage startedMs completedMs requestPendingAtStart facts')
        integer(row['startedMs'])
        integer(row['completedMs'], row['startedMs'], native['elapsedMs'])
        last = index == len(timing['samples']) - 1
        require(row['stage'] == ('after-settlement' if last else 'pending-timer'))
        require(type(row['requestPendingAtStart']) is bool and row['requestPendingAtStart'] is (not last))
        require(row['startedMs'] >= timing['settledMs'] if last else row['startedMs'] <= timing['settledMs'])
        log_facts(row['facts'])


def receipt(directory):
    raw = read(directory / 'post-verdict.manifest.json', 2048)
    manifest(raw)
    native = read(directory / 'post-verdict-native.json')
    timing = read(directory / 'post-verdict-timing.json')
    validate_failure(decode(native), decode(timing))
    return {'protocol': 'post-verdict-failure-1', 'manifestSha256': hashlib.sha256(raw).hexdigest(),
            'nativeSha256': hashlib.sha256(native).hexdigest(), 'timingSha256': hashlib.sha256(timing).hexdigest(),
            'nativeTestsRun': 1, 'nativeTestsPassed': 0, 'nodeExitedNonzero': True,
            'copyFlushAcknowledged': True, 'verdict': 'failed'}


if __name__ == '__main__':
    try:
        sys.stdout.buffer.write(canonical(receipt(Path(sys.argv[1]))))
    except (ValueError, OSError, KeyError, TypeError, IndexError):
        print('POST_VERDICT_REJECTED', file=sys.stderr)
        sys.exit(1)
