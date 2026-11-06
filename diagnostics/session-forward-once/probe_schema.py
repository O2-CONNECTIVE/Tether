"""Closed probe schema; historical stage projection is explicitly separate from capture."""
import json

VARIANT = 'tauri-driver-2.1.0-connection-probe-1'
TERMINALS = {'native-headers', 'native-error', 'request-dropped'}


def need(value):
    if not value:
        raise ValueError('PROBE_RECORD_REJECTED')


def unique(pairs):
    value = {}
    for key, item in pairs:
        need(key not in value)
        value[key] = item
    return value


def inspect(data, original_inspect):
    need(type(data) is bytes and len(data) <= 8192)
    lines = data.splitlines()
    need(2 <= len(lines) <= 34 and all(len(line) <= 256 for line in lines))
    rows = [json.loads(line, object_pairs_hook=unique) for line in lines]
    need(rows[0] == {'type': 'header', 'schema': 1, 'variant': VARIANT})
    need(type(rows[0]['schema']) is int)
    projected = [{'type': 'header', 'schema': 1, 'variant': 'tauri-driver-2.1.0-stage-1'}]
    terminals = []
    for value in rows[1:-1]:
        need(type(value) is dict and set(value) == {'type', 'seq', 'connection', 'request',
             'stage', 'state', 'elapsedUs', 'error', 'connectionAssigned'})
        assigned = value['connectionAssigned']
        need(assigned is None or type(assigned) is bool)
        if value['stage'] in TERMINALS:
            need(value['stage'] != 'native-headers' or assigned is not False)
            terminals.append(value)
        else:
            need(assigned is None)
        projected.append({key: item for key, item in value.items() if key != 'connectionAssigned'})
    projected.append(rows[-1])
    stage = original_inspect('\n'.join(json.dumps(row, separators=(',', ':')) for row in projected).encode())
    need(len(terminals) <= 1)
    terminal = terminals[0] if terminals else None
    complete = stage['diagnostic'] == 'complete'
    assigned = terminal['connectionAssigned'] if complete and terminal else None
    capture = {'schema': 1, 'check': 'native-connection-assignment', 'variant': VARIANT,
        'stageProjection': 'original-stage-schema', 'traceComplete': complete,
        'captureComplete': complete and type(assigned) is bool,
        'terminalStage': terminal['stage'] if complete and terminal else 'unconfirmed',
        'connection': terminal['connection'] if complete and terminal else None,
        'request': terminal['request'] if complete and terminal else None,
        'connectionAssigned': assigned,
        'classification': 'assigned-at-terminal' if assigned is True else
                          'not-observed-at-terminal' if assigned is False else 'unavailable'}
    return stage, capture


def unavailable():
    return {'schema': 1, 'check': 'native-connection-assignment', 'variant': VARIANT,
        'stageProjection': 'original-stage-schema', 'traceComplete': False,
        'captureComplete': False, 'terminalStage': 'unconfirmed', 'connection': None,
        'request': None, 'connectionAssigned': None, 'classification': 'unavailable'}


def validate_capture(value):
    expected = unavailable()
    need(type(value) is dict and set(value) == set(expected))
    for key in ('schema', 'check', 'variant', 'stageProjection'):
        need(type(value[key]) is type(expected[key]) and value[key] == expected[key])
    need(type(value['traceComplete']) is bool and type(value['captureComplete']) is bool)
    need(value['connectionAssigned'] is None or type(value['connectionAssigned']) is bool)
    need(value['terminalStage'] in TERMINALS | {'unconfirmed'})
    for key in ('connection', 'request'):
        need(value[key] is None or type(value[key]) is int and 1 <= value[key] <= 32)
    if not value['traceComplete']:
        need(value == expected)
    else:
        need(value['terminalStage'] in TERMINALS and value['connection'] is not None and value['request'] is not None)
        need(value['terminalStage'] != 'native-headers' or value['connectionAssigned'] is not False)
        assigned = value['connectionAssigned']
        need(value['captureComplete'] == (type(assigned) is bool))
        need(value['classification'] == ('assigned-at-terminal' if assigned is True else
             'not-observed-at-terminal' if assigned is False else 'unavailable'))
    return value
