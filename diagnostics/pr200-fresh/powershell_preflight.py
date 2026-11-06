"""Bounded, sequential PowerShell source/fixture preflight; no native calls."""
import os
import subprocess
import sys

import report


def legacy_safe(value):
    report.need(isinstance(value, dict) and set(value) == {'schema', 'kind', 'runtimeExecuted',
        'checksExpected', 'checksVerified', 'exitCode', 'passed', 'code'})
    report.need(type(value['schema']) is int and value['schema'] == 1 and value['kind'] == 'synthetic-powershell-ledger')
    report.need(type(value['runtimeExecuted']) is bool and type(value['passed']) is bool)
    report.need(type(value['checksExpected']) is int and value['checksExpected'] == 5 and
                type(value['checksVerified']) is int and value['checksVerified'] in {0, 5})
    report.need(value['exitCode'] is None or type(value['exitCode']) is int and -2147483648 <= value['exitCode'] <= 4294967295)
    report.need(value['code'] in {'pass', 'fixture-rejected', 'command-timeout', 'command-unavailable'})
    report.need(value['passed'] is (value['code'] == 'pass'))
    if value['passed']:
        report.need(value['runtimeExecuted'] and value['checksVerified'] == 5 and value['exitCode'] == 0)
    else:
        report.need(value['checksVerified'] == 0)
    return value


STAGES = ('parse', 'ledger', 'detail')
SCRIPTS = ('parse-predicate-sources.ps1', 'observer-reasons.test.ps1', 'observer-detail.test.ps1')
MARKERS = ('POWERSHELL_PREDICATE_PARSE_PASS files=5', 'OBSERVER_LEDGER_SYNTHETIC_PASS checks=5', 'OBSERVER_DETAIL_SYNTHETIC_PASS')


ASSERTION_CODES = {
    'SYNTHETIC_LOCATION_FAILED', 'UNTRUSTED_SOURCE_ACCEPTED', 'UNKNOWN_LOCATION_ACCEPTED',
    'OBSERVER_SOURCE_SYNTAX_FAILED', 'LEDGER_SEAM_MISSING', 'DETAIL_UNEXPECTED_OUTPUT',
    'DETAIL_DEDUP_FAILED', 'DETAIL_REDACTION_FAILED', 'DETAIL_BOUND_FAILED',
    'METADATA_GUARD_SEAM_MISSING', 'METADATA_SHORT_CIRCUIT_CHANGED', 'METADATA_VERDICT_CHANGED',
    'METADATA_ERROR_CHANGED', 'METADATA_ERROR_IDENTITY_CHANGED'}
ASSERTION_PREFIX = 'OBSERVER_DETAIL_ASSERTION_FAILED code='
ASSERTION_STATUSES = {'known', 'missing', 'unknown', 'conflicting', 'truncated', 'not-observed', 'not-applicable'}


def assertion_safe(value):
    report.need(isinstance(value, dict) and set(value) == {'status', 'code'})
    report.need(isinstance(value['status'], str) and value['status'] in ASSERTION_STATUSES)
    report.need(value['code'] is None or isinstance(value['code'], str) and value['code'] in ASSERTION_CODES)
    report.need((value['status'] == 'known') == (value['code'] is not None))
    return value


def assertion_output(raw):
    """Decode only this fixture's fixed marker; never return any input substring."""
    result = {'status': 'unknown', 'code': None}
    if len(raw) > 4096:
        return {'status': 'truncated', 'code': None}
    try:
        lines = raw.decode('utf-8-sig').splitlines()
    except UnicodeError:
        return result
    if not lines or all(not line for line in lines):
        return {'status': 'missing', 'code': None}
    markers = [line for line in lines if line.startswith(ASSERTION_PREFIX)]
    if len(markers) > 1 or markers and MARKERS[2] in lines:
        return {'status': 'conflicting', 'code': None}
    if len(lines) == 1 and len(markers) == 1:
        code = markers[0][len(ASSERTION_PREFIX):]
        if code in ASSERTION_CODES:
            return {'status': 'known', 'code': code}
    return result


def detail_assertion(private, stage):
    if stage['passed']:
        return {'status': 'not-applicable', 'code': None}
    if stage['code'] != 'fixture-rejected':
        return {'status': 'not-observed', 'code': None}
    try:
        with (private / 'stdout.log').open('rb') as stream:
            value = assertion_output(stream.read(4097))
    except Exception:
        return {'status': 'not-observed', 'code': None}
    if value['status'] == 'known' and stage['exitCode'] != 1:
        return {'status': 'conflicting', 'code': None}
    return assertion_safe(value)


def stage_safe(value):
    report.need(isinstance(value, dict) and set(value) == {'runtimeExecuted', 'exitCode', 'passed', 'code'})
    report.need(type(value['runtimeExecuted']) is bool and type(value['passed']) is bool)
    report.need(value['exitCode'] is None or type(value['exitCode']) is int and -2147483648 <= value['exitCode'] <= 4294967295)
    report.need(value['code'] in {'not-run', 'pass', 'fixture-rejected', 'command-timeout', 'command-unavailable'})
    report.need(value['passed'] is (value['code'] == 'pass'))
    if value['code'] in {'not-run', 'command-unavailable'}:
        report.need(not value['runtimeExecuted'] and value['exitCode'] is None)
    elif value['code'] == 'command-timeout':
        report.need(value['runtimeExecuted'] and value['exitCode'] is None)
    else:
        report.need(value['runtimeExecuted'] and type(value['exitCode']) is int)
        report.need(not value['passed'] or value['exitCode'] == 0)
    return value


def preflight_safe(value):
    if isinstance(value, dict) and value.get('schema') == 1:
        return legacy_safe(value)  # Retained history only; cannot authorize a new trial.
    report.need(isinstance(value, dict) and type(value.get('schema')) is int and value['schema'] in {2, 3})
    fields = {'schema', 'kind', 'parseFilesExpected', 'parseFilesVerified', 'fixturesExpected', 'fixturesVerified', 'passed', 'stages'}
    report.need(set(value) == (fields | {'detailAssertion'} if value['schema'] == 3 else fields))
    report.need(value['kind'] == 'predicate-powershell-preflight')
    report.need(type(value['parseFilesExpected']) is int and value['parseFilesExpected'] == 5)
    report.need(type(value['parseFilesVerified']) is int and value['parseFilesVerified'] in {0, 5})
    report.need(type(value['fixturesExpected']) is int and value['fixturesExpected'] == 2)
    report.need(type(value['fixturesVerified']) is int and 0 <= value['fixturesVerified'] <= 2)
    report.need(type(value['passed']) is bool and isinstance(value['stages'], dict) and set(value['stages']) == set(STAGES))
    stages = {key: stage_safe(value['stages'][key]) for key in STAGES}
    report.need(value['parseFilesVerified'] == (5 if stages['parse']['passed'] else 0))
    report.need(value['fixturesVerified'] == sum(stages[key]['passed'] for key in ('ledger', 'detail')))
    report.need(value['passed'] is all(stage['passed'] for stage in stages.values()))
    if not stages['parse']['passed']:
        report.need(all(stages[key]['code'] == 'not-run' for key in ('ledger', 'detail')))
    if not stages['ledger']['passed']:
        report.need(stages['detail']['code'] == 'not-run')
    if value['schema'] == 3:
        assertion = assertion_safe(value['detailAssertion'])
        detail = stages['detail']
        if detail['passed']:
            report.need(assertion['status'] == 'not-applicable')
        elif detail['code'] != 'fixture-rejected':
            report.need(assertion['status'] == 'not-observed')
        else:
            report.need(assertion['status'] != 'not-applicable')
            report.need(assertion['status'] != 'known' or detail['exitCode'] == 1)
    return value


def current_passed(value):
    return preflight_safe(value)['schema'] == 3 and value['passed']


def run_stage(script, marker, private):
    result = {'runtimeExecuted': False, 'exitCode': None, 'passed': False, 'code': 'command-unavailable'}
    try:
        with (private / 'stdout.log').open('wb') as stdout, (private / 'stderr.log').open('wb') as stderr:
            process = subprocess.run(['pwsh', '-NoLogo', '-NoProfile', '-File', str(script)],
                stdout=stdout, stderr=stderr, timeout=60, check=False)
        result.update(runtimeExecuted=True, exitCode=process.returncode, code='fixture-rejected')
        raw = (private / 'stdout.log').read_bytes() if (private / 'stdout.log').stat().st_size <= 4096 else b''
        if (process.returncode == 0 and raw.decode('utf-8-sig').strip() == marker and (private / 'stderr.log').stat().st_size == 0):
            result.update(passed=True, code='pass')
    except subprocess.TimeoutExpired:
        result.update(runtimeExecuted=True, code='command-timeout')
    except Exception:
        pass  # Never copy raw exception/log data; missing output cannot pass.
    return stage_safe(result)


def preflight(control, root):
    report.need(sys.platform == 'win32' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted')
    private = root / 'powershell-predicate-preflight'
    private.mkdir(exist_ok=False)
    result = {'schema': 3, 'kind': 'predicate-powershell-preflight', 'parseFilesExpected': 5, 'parseFilesVerified': 0,
        'detailAssertion': {'status': 'not-observed', 'code': None},
        'fixturesExpected': 2, 'fixturesVerified': 0, 'passed': False, 'stages': {key: {
            'runtimeExecuted': False, 'exitCode': None, 'passed': False, 'code': 'not-run'} for key in STAGES}}
    for key, script, marker in zip(STAGES, SCRIPTS, MARKERS):
        folder = private / key
        folder.mkdir(exist_ok=False)
        result['stages'][key] = run_stage(control / 'diagnostics/pr200-fresh' / script, marker, folder)
        if key == 'detail':
            result['detailAssertion'] = detail_assertion(folder, result['stages'][key])
        if key == 'parse':
            result['parseFilesVerified'] = 5 if result['stages'][key]['passed'] else 0
        else:
            result['fixturesVerified'] += int(result['stages'][key]['passed'])
        result['passed'] = all(stage['passed'] for stage in result['stages'].values())
        report.write(root / 'safe/powershell-preflight.json', preflight_safe(result))
        if not result['stages'][key]['passed']:
            break
    result['passed'] = all(stage['passed'] for stage in result['stages'].values())
    report.write(root / 'safe/powershell-preflight.json', preflight_safe(result))
    print('POWERSHELL_PREDICATE_PREFLIGHT_PASS' if result['passed'] else 'POWERSHELL_PREDICATE_PREFLIGHT_FAILED')
    return 0 if result['passed'] else 1
