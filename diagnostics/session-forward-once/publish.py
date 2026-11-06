"""Bounded, closed-schema publication after the only observation/cleanup opportunity."""
import json
import os
from pathlib import Path
import re
import driver
import legacy
import overlay
import pins
import probe_schema

IDENTITY = {'schema', 'product', 'productTree', 'diagnostic', 'diagnosticTree', 'control',
    'controlTree', 'base', 'overlaySha256', 'overlayFileCount', 'supervisorSha256', 'setupSha256',
    'exeSha256', 'driverSha256', 'cargoLockSha256', 'instrumentationEnabled', 'productFeatures',
    'productProfile', 'nativeRequestDeadlineMs'}
FILE_NAMES = {'run-status.json', 'build-identity.json', 'launch-identity.json', 'final-identity.json',
    'cache.json', 'transport-preflight.json', 'runtime-versions.json', 'trial-exits.json',
    'driver-provenance.json', 'native-connection-record.json', 'driver-stage-report.json',
    'inventory.json', 'cleanup-audit.json', 'native-result.json', 'native-observation.json',
    'session-startup-timing.json', 'webview-result.json', 'outcome.json', 'command-ledger.json', 'core-preflight.json'}


def identity(value, source):
    overlay.need(type(value) is dict and set(value) == IDENTITY)
    fixed = {'schema': 1, 'product': pins.PRODUCT, 'productTree': pins.PRODUCT_TREE,
        'diagnostic': pins.DIAGNOSTIC, 'diagnosticTree': pins.DIAGNOSTIC_TREE,
        'control': os.environ['EXPECTED_CONTROL'], 'base': pins.BASE_SHA, 'cargoLockSha256': pins.LOCK,
        'instrumentationEnabled': True, 'productFeatures': 'custom-protocol',
        'productProfile': 'debug', 'nativeRequestDeadlineMs': 15000}
    overlay.need(all(type(value[k]) is type(v) and value[k] == v for k, v in fixed.items()))
    overlay.need(value['controlTree'] == overlay.git(source / '.control', 'rev-parse', 'HEAD^{tree}'))
    overlay.need(type(value['overlayFileCount']) is int and 1 <= value['overlayFileCount'] <= 100)
    for key in ('overlaySha256', 'supervisorSha256', 'setupSha256', 'exeSha256', 'driverSha256'):
        overlay.need(value[key] is None and key in {'exeSha256', 'driverSha256'} or
                     type(value[key]) is str and re.fullmatch('[0-9a-f]{64}', value[key]))
    return value


def cache(value):
    overlay.need(type(value) is dict and set(value) == {'schema', 'keySha256Suffix', 'lookupExact', 'exactHit', 'restoreOnly', 'state'})
    overlay.need(type(value['schema']) is int and value['schema'] == 1 and value['restoreOnly'] is True)
    overlay.need(type(value['keySha256Suffix']) is str and re.fullmatch('[0-9a-f]{64}', value['keySha256Suffix']))
    overlay.need(type(value['lookupExact']) is bool and type(value['exactHit']) is bool)
    expected = 'exact-hit' if value['lookupExact'] and value['exactHit'] else 'miss' if not value['lookupExact'] and not value['exactHit'] else 'restore-mismatch'
    overlay.need(value['state'] == expected or value['state'] == 'restore-mismatch')
    return value


def transport(value):
    overlay.need(type(value) is dict and set(value) == {'schema', 'fixedPython', 'realHandler', 'fixedPythonPassed', 'realHandlerAndRecorderPassed', 'nativeTrials'})
    overlay.need(type(value['schema']) is int and value['schema'] == 1 and type(value['nativeTrials']) is int and value['nativeTrials'] == 0)
    for stage, count, maximum in [('fixedPython', 'fixedPythonPassed', 5), ('realHandler', 'realHandlerAndRecorderPassed', 10)]:
        overlay.need(value[stage] in {'not-run', 'running', 'passed', 'failed', 'timeout'})
        overlay.need(type(value[count]) is int and value[count] == (maximum if value[stage] == 'passed' else 0))
    overlay.need(value['realHandler'] == 'not-run' or value['fixedPython'] == 'passed')
    return value


def versions(value):
    overlay.need(type(value) is dict and set(value) == {'schema', 'python', 'node', 'rust', 'powershell'})
    overlay.need(type(value['schema']) is int and value['schema'] == 1)
    overlay.need(value['python'] == '3.13.17' and value['node'] == '24.22.1' and value['rust'] in {None, '1.99.1'})
    overlay.need(type(value['powershell']) is str and re.fullmatch(r'\d{1,3}(?:\.\d{1,5}){2,3}', value['powershell']))
    return value


def core_preflight(value):
    overlay.need(type(value) is dict and set(value) == {'schema', 'controlPythonPassed', 'originalPythonPassed', 'originalNodePassed', 'powershellFilesParsed'})
    for key, expected in {'schema': 1, 'controlPythonPassed': 23, 'originalPythonPassed': 10, 'originalNodePassed': 15}.items():
        overlay.need(type(value[key]) is int and value[key] == expected)
    overlay.need(type(value['powershellFilesParsed']) is int and 1 <= value['powershellFilesParsed'] <= 80)
    return value


def boolean_record(value):
    fields = {'schema', 'supervisorSucceeded', 'inspectorSucceeded', 'auditSucceeded', 'identityStable'}
    overlay.need(type(value) is dict and set(value) == fields and type(value['schema']) is int and value['schema'] == 1)
    overlay.need(all(value[k] is None or type(value[k]) is bool for k in fields - {'schema'}))
    return value


def provenance(value, root):
    archive = root / 'tools/tauri-driver.crate'
    _, _, _, stage2, patch = driver.images(root, archive)
    binary = root / 'tools/tauri/bin/tauri-driver.exe'
    expected = driver.provenance(root, archive, stage2, patch, binary)
    overlay.need(type(value) is dict and value == expected and type(value['schema']) is int)
    return value


def native_file(name, value):
    legacy.safe(value)
    overlay.need(type(value) is dict)
    if name == 'native-observation.json':
        overlay.need(type(value.get('requests')) is list and all(type(r) is dict for r in value['requests']))
        overlay.need(type(value.get('droppedRequests')) is int and value['droppedRequests'] >= 0)
        for row in value['requests']:
            overlay.need(type(row.get('elapsedMs')) is int and row['elapsedMs'] >= 0)
            overlay.need('status' not in row or type(row['status']) is int and 100 <= row['status'] <= 599)
    if name == 'native-result.json':
        overlay.need(all(type(value.get(k)) is int and value[k] in {0, 1} for k in ('nativeTestsRun', 'nativeTestsPassed')))
        overlay.need(type(value.get('normalCloseVerified')) is bool and type(value.get('cleanupStarted')) is bool)
        for key in ('cleanupFailures', 'childHandleDisposeFailures'):
            overlay.need(type(value.get(key)) is int and 0 <= value[key] <= 4096)
        if 'afterDriverCleanupFacts' in value:
            children = value['afterDriverCleanupFacts']
            overlay.need(type(children) is dict and type(children.get('rows')) is list and all(type(r) is dict for r in children['rows']))
            for key in ('observed', 'remainingSnapshot', 'omittedRows'):
                overlay.need(children.get(key) is None or type(children[key]) is int and 0 <= children[key] <= 4096)
            for row in children['rows']:
                overlay.need(row.get('snapshotState') is None or type(row['snapshotState']) is str)
    if name == 'cleanup-audit.json':
        overlay.need(set(value) == {'queryComplete', 'appRemaining', 'driversRemaining', 'profileProcessesRemaining', 'harnessNodesRemaining', 'listenersRemaining'})
        overlay.need(type(value['queryComplete']) is bool)
        overlay.need(all(value[k] is None or type(value[k]) is int and 0 <= value[k] <= 4096 for k in value if k != 'queryComplete'))
    return value


def cleanup(result, audit):
    children = result.get('afterDriverCleanupFacts', {})
    child_evidence = not result.get('normalCloseVerified') or (
        type(children.get('observed')) is int and children['observed'] > 0 and
        children.get('remainingSnapshot') == 0 and children.get('omittedRows') == 0 and
        len(children.get('rows', [])) == children['observed'] and all(
            row.get('handleIdentityVerified') is True and row.get('handleState') == 'exited' and
            row.get('snapshotState') in {'absent', 'pid-reused'} for row in children['rows']))
    return bool(child_evidence and result.get('cleanupStarted') is True and result.get('cleanupFailures') == 0 and
        result.get('childHandleDisposeFailures') == 0 and result.get('driverExitObservedAfterCleanup') is True and
        audit.get('queryComplete') is True and all(audit.get(k) == 0 for k in
        ('appRemaining', 'driversRemaining', 'profileProcessesRemaining', 'harnessNodesRemaining', 'listenersRemaining')))


def decision(files, state, stable, rejected):
    result = files.get('native-result.json', {})
    observation = files.get('native-observation.json', {})
    trace = files.get('driver-stage-report.json', {})
    capture = files.get('native-connection-record.json', probe_schema.unavailable())
    ends = [r for r in observation.get('requests', []) if r.get('operation') == 'create-session' and r.get('outcome') != 'started']
    request = ends[0] if len(ends) == 1 else {}
    match = (observation.get('phase') == 'session-create' and observation.get('status') == 'failed' and
        observation.get('errorCode') == 'NATIVE_DRIVER_TIMEOUT' and request.get('outcome') == 'timeout' and
        'status' not in request and observation.get('documentObservations') == 0 and
        observation.get('originClass') == observation.get('titleClass') == 'unobserved' and
        trace.get('diagnostic') == 'complete' and trace.get('requestStage') == 'dropped-before-headers')
    clean = cleanup(result, files.get('cleanup-audit.json', {}))
    exits = files.get('trial-exits.json', {})
    complete = (all(state['stages'][s] == 'passed' for s in state['stages']) and
        stable and not rejected and capture['captureComplete'] and exits and all(
        exits.get(k) is True for k in ('supervisorSucceeded', 'inspectorSucceeded', 'auditSucceeded', 'identityStable')))
    native = None
    if exits.get('supervisorSucceeded') is False:
        native = False
    elif exits.get('supervisorSucceeded') is True:
        verified = (result.get('nativeTestsRun') == result.get('nativeTestsPassed') == 1 and
            result.get('normalCloseVerified') is True and result.get('cleanupFailures') == 0 and
            result.get('childHandleDisposeFailures') == 0)
        native = True if verified else None
    passed = bool(complete and native is True and len(ends) == 1 and request.get('outcome') == 'ok' and
        request.get('status') == 200 and observation.get('droppedRequests') == 0 and
        result.get('nativeTestsRun') == result.get('nativeTestsPassed') == 1 and
        result.get('normalCloseVerified') is True and clean and observation.get('status') == 'webview-passed-awaiting-close' and
        trace.get('requestStage') == 'native-headers' and capture['connectionAssigned'] is True)
    count = result.get('nativeTestsRun')
    conflict = not state['nativeInvoked'] and count not in {None, 0}
    if not state['nativeInvoked'] and count is None:
        count = 0
    overlay.need(count is None or type(count) is int and count in {0, 1})
    status = request.get('status')
    overlay.need(status is None or type(status) is int and 100 <= status <= 599)
    return {'schema': 1, 'diagnosticOnly': True, 'runnerCap': 1, 'nativeCap': 1,
        'nativeInvoked': state['nativeInvoked'], 'nativeTestsObserved': count,
        'nativeCountConflict': conflict,
        'supervisorSucceeded': exits.get('supervisorSucceeded'), 'nativeSucceeded': native,
        'diagnosticSucceeded': passed, 'matchingTimeout': bool(match), 'cleanupConfirmed': clean,
        'normalCloseVerified': result.get('normalCloseVerified') is True,
        'createSessionMs': request.get('elapsedMs'), 'createSessionHttpStatus': status,
        'connectionAssigned': capture['connectionAssigned'], 'captureComplete': capture['captureComplete'],
        'sourceAndSpecimenStable': stable, 'rejectedFiles': rejected,
        'interpretation': 'matching-timeout-new-specimen' if match else 'no-repro-inconclusive' if passed else
            'preflight-failed-native-not-started' if not state['nativeInvoked'] and not conflict else 'failure-or-uncertain',
        'originalFailuresPreserved': True, 'rootCauseEstablished': False, 'timeoutFixed': False}, passed


def missing(files, state):
    if not state['nativeInvoked']:
        return []
    required = FILE_NAMES - {'outcome.json', 'webview-result.json'}
    if files.get('native-result.json', {}).get('nativeTestsPassed') == 1:
        required = required | {'webview-result.json'}
    return sorted(required - set(files))


def encode(files):
    overlay.need(type(files) is dict and set(files) <= FILE_NAMES)
    hashes = {n: overlay.sha(json.dumps(v, sort_keys=True, separators=(',', ':')).encode()) for n, v in files.items()}
    record = {'schema': 1, 'hashBasis': 'canonical-sorted-json-no-newline', 'files': files, 'fileSha256': hashes}
    body = json.dumps(record, sort_keys=True, separators=(',', ':')).encode()
    overlay.need(len(files) <= 20 and len(body) <= 65536)
    return body


def uncertain(state, stable, rejected, files):
    # Preserve validated siblings and the original authority checkpoint, never
    # serialize the exception or invent a native failure from missing evidence.
    return {'schema': 1, 'diagnosticOnly': True, 'runnerCap': 1, 'nativeCap': 1,
        'nativeInvoked': state['nativeInvoked'], 'nativeTestsObserved': None if state['nativeInvoked'] else 0,
        'nativeCountConflict': None, 'supervisorSucceeded': files.get('trial-exits.json', {}).get('supervisorSucceeded'),
        'nativeSucceeded': None, 'diagnosticSucceeded': False, 'matchingTimeout': None,
        'cleanupConfirmed': False, 'normalCloseVerified': None, 'createSessionMs': None,
        'createSessionHttpStatus': None, 'connectionAssigned': None, 'captureComplete': False,
        'sourceAndSpecimenStable': stable, 'rejectedFiles': rejected,
        'interpretation': 'evidence-evaluation-uncertain', 'originalFailuresPreserved': True,
        'rootCauseEstablished': False, 'timeoutFixed': False}


def finalize(source, root):
    import runner
    files, rejected = {}, []
    try:
        runner.allow_native_codes(source)
    except Exception:
        # Closed static words still apply; unknown native codes reject that file.
        pass
    state = runner.validate_state(read_safe(root / 'safe/run-status.json'))
    if state['nativeInvoked'] and not state['auditInvoked']:
        try:
            runner.audit(source, root, state)
        except Exception:
            pass
    stable = True
    try:
        runner.identity(source, root, True)
    except Exception:
        stable = False
    candidates = [(root / 'safe/run-status.json', runner.validate_state),
        *[(root / 'safe' / n, lambda v: identity(v, source)) for n in
          ('build-identity.json', 'launch-identity.json', 'final-identity.json')],
        (root / 'safe/cache.json', cache), (root / 'safe/transport-preflight.json', transport),
        (root / 'safe/runtime-versions.json', versions),
        (root / 'safe/core-preflight.json', core_preflight),
        (root / 'safe/command-ledger.json', runner.validate_ledger),
        (root / 'safe/trial-exits.json', boolean_record),
        (root / 'safe/driver-provenance.json', lambda v: provenance(v, root)),
        (root / 'safe/native-connection-record.json', probe_schema.validate_capture),
        (root / 'safe/driver-stage-report.json', lambda v: native_file('driver-stage-report.json', v)),
        (root / 'inventory.json', lambda v: native_file('inventory.json', v)),
        (root / 'audit.json', lambda v: native_file('cleanup-audit.json', v)),
        *[(root / 'observation' / n, lambda v, name=n: native_file(name, v)) for n in runner.NATIVE_FILES]]
    for path, validator in candidates:
        if not path.exists():
            continue
        name = 'cleanup-audit.json' if path.name == 'audit.json' else path.name
        try:
            files[name] = validator(read_safe(path))
        except Exception:
            rejected.append(name)
    if state['nativeInvoked']:
        rejected = sorted(set(rejected) | set(missing(files, state)))
        try:
            original = runner.module('publication_original_trace', source / 'scripts/verify_driver_trace.py')
            trace = root / 'tools/driver-stage.jsonl'
            overlay.need(trace.is_file() and not trace.is_symlink() and trace.stat().st_size <= 8192)
            stage, capture = probe_schema.inspect(trace.read_bytes(), original.inspect)
            overlay.need(files.get('driver-stage-report.json') == stage)
            overlay.need(files.get('native-connection-record.json') == capture)
        except Exception:
            rejected = sorted(set(rejected) | {'driver-stage-report.json'})
    try:
        summary, passed = decision(files, state, stable, sorted(rejected))
    except Exception:
        summary, passed = uncertain(state, stable, sorted(rejected), files), False
    sites, valid_sites = runner.rejection_facts(root, state, stable)
    summary['rejectionSites'] = sites
    summary['rejectionEvidenceValid'] = valid_sites
    if not valid_sites or any(v is not None for v in sites.values()):
        passed = False
        summary['diagnosticSucceeded'] = False
        if summary['interpretation'] == 'no-repro-inconclusive':
            summary['interpretation'] = 'failure-or-uncertain'
    files['outcome.json'] = summary
    body = encode(files)
    digest = overlay.sha(body)
    block = f'FORWARD_SAFE_RECORD_BEGIN sha256={digest} bytes={len(body)} files={len(files)}\n' + body.decode() + '\nFORWARD_SAFE_RECORD_END\n'
    (root / 'safe/canonical-record.json').write_bytes(body)
    print(block, end='', flush=True)
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a', encoding='utf-8') as stream:
        stream.write('Diagnostic only. Original timeout remains unresolved.\n\n```text\n' + block + '```\n')
    return 0 if passed else 1


def read_safe(path):
    return legacy.read(path)
