"""Fixed-source report reader derived from reviewed phase1; contains no launch loop."""


import hashlib


import json


import math


import os




import re




import subprocess


import sys


SOURCE = 'c6c5ae5342fcde6b1bf77eff744ad48ddce5b8da'


TREE = 'e278496a55b4accc6caafa7a7a99538e5c8ceeca'


CAP = 1


EXE = 'evaluation-unpacked/RescueHostTauriUnwiredEvaluation/rescue-host-shell.exe'


ARCHIVE = 'evaluation-package/RescueHostTauriUnwiredEvaluation-x64.zip'


FILES = ('native-result.json', 'native-observation.json', 'session-startup-timing.json',
         'driver-startup-log-facts.json', 'webview-result.json',
         'driver-stage-report.json', 'driver-provenance.json', 'cleanup-audit.json')


KEYS = set('''schema kind status phase elapsedMs documentObservations documentStates errorCode
originClass titleClass droppedRequests phases requests lastRequest operation outcome driverError
nativeTestsRun nativeTestsPassed cleanupStarted normalCloseVerified failurePhase listenerFacts
port loopback family owned beforeSessionFacts beforeSessionObservation driverIdentity edgeIdentity appCandidates
lineageCorrelation correlatedApps correlatedWebviews listeners omittedListeners failureCode
failureType failureScript failureLine startupFailureFacts startupFailureObservation
startupFailureQueryMs driverStageFileObserved cleanupFailures cleanupCodes
driverExitObservedAfterCleanup childHandleDisposeFailures startupAfterCleanupFacts
startupAfterCleanupObservation initialLineageFacts childHandleOwner webviewChildrenObserved
appWaitElapsedMs appCloseObservedBeforeCleanup appExitCode childSnapshotCount childSnapshotMaxMs
childSnapshotStartMs childSnapshotEndMs childWaitElapsedMs childCloseFacts afterWaitProbeFacts
afterWaitProbeElapsedMs afterWaitProbeObservation closeObservedBeforeCleanup driverStableThroughClose
browserVersion afterDriverCleanupFacts afterDriverCleanupObservation observed remainingSnapshot
omittedRows rows handleIdentityVerified webviewType exitRelativeMs profileMatch cimCreationMatch
slot role parentSlot creationKnown parentOrder snapshotState handleState reason parentCreationKnown
childCreationKnown childMinusParentCreationMs childRole searchRoot parentRole parentDepth
parentSnapshotIdentity childSnapshotIdentity timeOrder parentBeforeDriverIdentity childBeforeDriverIdentity
ownerMatchesRecordedPid purpose settledMs samples stage startedMs completedMs requestPendingAtStart
facts stdout stderr bytesRead truncated hints proxyErrorKinds proxyConnectionError sessionNotCreated
devtoolsPortMissing profileInUse browserNotReachable browserExited versionMismatch check diagnostic code
observedStages requestStage connectionEnd events summary type seq connection request state elapsedUs
error attempted enqueued lastSequence pendingRequests sealed written dropped variant upstreamSha256
patchSha256 diagnosticCodeSha256 preimage postimage postimageManifestSha256 upstreamFileCount
cargoLockSha256 binarySha256 nativeTestsStarted checks actualRustIpc keyboard cycles refreshes
runtimeVersions edgeDriverVersion edgeDriverSha256 runnerImage queryComplete appRemaining
driversRemaining profileProcessesRemaining harnessNodesRemaining listenersRemaining
diagnostic.rs diagnostic_tests.rs src/diagnostic.rs src/diagnostic_tests.rs src/main.rs src/server.rs'''.split())


WORDS = set('''windows-webview2-native-smoke running failed webview-passed-awaiting-close bootstrap
session-create origin-check readiness main-screenshot unwired-contract keyboard diagnostics-screenshot
result-write complete unobserved bundled initial other checking unwired unavailable absent
bundled-loading bundled-ready rejected create-session execute-sync execute-async find-element
click-element keys screenshot started ok timeout transport http webdriver invalid-response
InterNetwork InterNetworkV6 ipv4 ipv6 same-identity different-identity ambiguous unrecorded
consistent unconfirmed webview-and-ipc environment ownership-confirmation normal-close
RuntimeException InvalidOperationException MethodInvocationException supervise.ps1
harness-read-only-before-app-close webview console-host crash-reporter windows-error-reporting
no-type-flag renderer gpu-process utility crashpad-handler child-older unknown equal child-newer
same different driver edge app app-candidate webview-name not-recorded not-in-current-snapshot
creation-unavailable parent-ambiguous parent-missing parent-cycle parent-older-than-child
pid-reused verified live exited not-found diagnostic-hints-only success pending-timer after-settlement
parent-row-ambiguous parent-creation-unavailable child-row-ambiguous child-creation-unavailable
read incomplete-message canceled io unclassified native-driver-stage TRACE_COMPLETE TRACE_INCOMPLETE
TRACE_PROVENANCE_UNAVAILABLE TRACE_INVALID native-headers native-error dropped-before-headers dropped-before-forward
clean incomplete none closed parse header event summary idle received forward-pending headers-returned
dropped connection-open session-received forward-start request-dropped connection-end
tauri-driver-2.1.0-stage-1 read-only-native-environment-inventory webview-observed-awaiting-normal-close
node-kill-failed driver-kill-failed driver-exit-unconfirmed driver-wait-failed handle-dispose-failed'''.split())


WORDS.update(['', 'session not created', 'invalid session id', 'javascript error', 'unknown error',
              'no such element', 'stale element reference', 'invalid argument', 'no such window'])


def need(value):
    if not value:
        raise ValueError('DIAGNOSTIC_BOUNDARY_REJECTED')


def digest(path):
    need(path.is_file() and not path.is_symlink())
    result = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            result.update(block)
    return result.hexdigest()


def unique(pairs):
    obj = {}
    for key, value in pairs:
        need(key not in obj)
        obj[key] = value
    return obj


def read(path):
    need(path.is_file() and not path.is_symlink() and path.stat().st_size <= 262144)
    return json.loads(path.read_text(encoding='utf-8-sig'), object_pairs_hook=unique)


def safe(value, depth=0):
    need(depth <= 12)
    if value is None or type(value) is bool:
        return value
    if type(value) in (int, float):
        need(math.isfinite(value) and abs(value) <= 1_000_000_000)
    elif isinstance(value, str):
        need(value in WORDS or re.fullmatch(r'[0-9a-f]{64}', value) or
             re.fullmatch(r'\d{1,8}(?:\.\d{1,8}){1,3}', value) or
             re.fullmatch(r'NATIVE_[A-Z_]{1,80}', value) or
             re.fullmatch(r'Microsoft Edge WebDriver \d+\.\d+\.\d+\.\d+ \([0-9a-f]{40}\)', value))
    elif isinstance(value, list):
        need(len(value) <= 512)
        for item in value:
            safe(item, depth + 1)
    elif isinstance(value, dict):
        need(len(value) <= 128 and set(value) <= KEYS)
        for item in value.values():
            safe(item, depth + 1)
    else:
        need(False)
    return value


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, sort_keys=True, indent=2) + '\n', encoding='utf-8')


def command(args, cwd=None, log=None, timeout=180):
    # No shell, no echoed command arguments, no raw output on the Actions console.
    with (log.open('wb') if log else open(os.devnull, 'wb')) as stream:
        return subprocess.run(args, cwd=cwd, stdout=stream, stderr=subprocess.STDOUT,
                              timeout=timeout, check=False).returncode


def git(source, *args):
    return subprocess.check_output(['git', '-C', str(source), *args],
                                   stderr=subprocess.DEVNULL, timeout=30).decode().strip()


def verify_source(source):
    need(git(source, 'rev-parse', 'HEAD') == SOURCE)
    need(git(source, 'rev-parse', 'HEAD^{tree}') == TREE)
    need(git(source, 'status', '--porcelain', '--untracked-files=no') == '')


def identity(source, control, root, observed_tools=False):
    verify_source(source)
    folder = root / 'safe'
    current = read(folder / 'build-identity.json') if (folder / 'build-identity.json').exists() else {}
    current.update(schema=1, diagnosticOnly=True, distribution=False, sourceCommit=SOURCE,
                   sourceTree=TREE, controlCommit=git(control, 'rev-parse', 'HEAD'),
                   features=['custom-protocol'], profile='release', specimen='new-build-not-original-binary',
                   requestDeadlineMs=15000, trialCap=CAP,
                   originalFailure={'run': 42037151371, 'job': 127278142941, 'attempt': 1})
    paths = {'cargoLockSha256': source / 'ui/tauri/src-tauri/Cargo.lock',
             'packageLockSha256': source / 'ui/tauri/package-lock.json',
             'toolPinsSha256': source / 'ui/tauri/native-smoke/tools.json',
             'supervisorSha256': source / 'ui/tauri/native-smoke/supervise.ps1',
             'builtExeSha256': root / 'target/release/rescue-host-shell.exe',
             'exeSha256': root / EXE, 'archiveSha256': root / ARCHIVE}
    current['hashes'] = {key: digest(path) if path.exists() else None for key, path in paths.items()}
    if (root / 'inventory.json').exists():
        current['inventory'] = safe(read(root / 'inventory.json'))
    if (root / 'tools/driver-provenance.json').exists():
        current['driverProvenance'] = safe(read(root / 'tools/driver-provenance.json'))
        current['edgeDriverSha256'] = digest(root / 'tools/edge/msedgedriver.exe')
    if observed_tools:
        versions = {}
        for name, args, pattern in [
            ('node', ['node', '--version'], r'v24\.22\.1'),
            ('python', [sys.executable, '--version'], r'Python 3\.13\.17'),
            ('rust', ['rustc', '+1.99.1', '--version'], r'rustc 1\.99\.1 \([0-9a-f]+ \d{4}-\d{2}-\d{2}\)'),
        ]:
            value = subprocess.check_output(args, stderr=subprocess.DEVNULL, timeout=30).decode().strip()
            need(re.fullmatch(pattern, value))
            versions[name] = value
        current['observedToolVersions'] = versions
    write(folder / 'build-identity.json', current)
    return current


def cleanup_confirmed(result, audit):
    children = result.get('afterDriverCleanupFacts', {})
    child_evidence = (not result.get('normalCloseVerified') or
                      (children.get('observed', 0) > 0 and children.get('remainingSnapshot') == 0 and
                       children.get('omittedRows') == 0 and len(children.get('rows', [])) == children['observed'] and
                       all(row.get('handleIdentityVerified') is True and row.get('handleState') == 'exited' and
                           row.get('snapshotState') in {'absent', 'pid-reused'} for row in children['rows'])))
    return (child_evidence and result.get('cleanupStarted') is True and result.get('cleanupFailures') == 0 and
            result.get('childHandleDisposeFailures') == 0 and
            result.get('driverExitObservedAfterCleanup') is True and
            audit.get('queryComplete') is True and all(audit.get(key) == 0 for key in
            ['appRemaining', 'driversRemaining', 'profileProcessesRemaining',
             'harnessNodesRemaining', 'listenersRemaining']))


def verdict(supervisor, inspector, audit_exit, reports):
    result = reports.get('native-result.json', {})
    obs = reports.get('native-observation.json', {})
    trace = reports.get('driver-stage-report.json', {})
    last = obs.get('lastRequest') or {}
    symptom = (obs.get('phase') == 'session-create' and obs.get('status') == 'failed' and
               obs.get('errorCode') == 'NATIVE_DRIVER_TIMEOUT' and
               last.get('operation') == 'create-session' and last.get('outcome') == 'timeout' and
               obs.get('documentObservations') == 0 and obs.get('originClass') == 'unobserved' and
               obs.get('titleClass') == 'unobserved')
    match = (symptom and trace.get('code') == 'TRACE_COMPLETE' and
             trace.get('requestStage') == 'dropped-before-headers' and
             {'session-received', 'forward-start', 'request-dropped'} <= set(trace.get('observedStages', [])) and
             not {'native-headers', 'native-error'} & set(trace.get('observedStages', [])))
    cleanup = cleanup_confirmed(result, reports.get('cleanup-audit.json', {})) and audit_exit == 0
    row = {'matchingTimeout': match, 'sessionTimeoutSymptom': symptom, 'cleanupConfirmed': cleanup,
           'normalCloseVerified': result.get('normalCloseVerified') is True,
           'supervisorExit': supervisor, 'inspectorExit': inspector, 'auditExit': audit_exit,
           'sessionElapsedMs': last.get('elapsedMs') if last.get('operation') == 'create-session' else
           next((r.get('elapsedMs') for r in obs.get('requests', [])
                 if r.get('operation') == 'create-session' and r.get('outcome') == 'ok'), None)}
    if match:
        reason = 'matching-session-timeout'
    elif not cleanup:
        reason = 'cleanup-uncertain-or-failed'
    elif (supervisor != 0 or inspector != 0 or result.get('nativeTestsRun') != 1 or
          result.get('nativeTestsPassed') != 1 or result.get('normalCloseVerified') is not True or
          obs.get('status') != 'webview-passed-awaiting-close' or
          trace.get('code') != 'TRACE_COMPLETE' or trace.get('requestStage') != 'native-headers'):
        reason = 'unexpected-failure'
    else:
        reason = 'continue'
    return row, reason


def aggregate(rows, reason):
    return {'schema': 1, 'diagnosticOnly': True, 'trialCap': CAP, 'attempted': len(rows),
            'trials': rows, 'stopReason': reason, 'pr200Blocked': True,
            'interpretation': 'reproduced-new-specimen' if any(r['matchingTimeout'] for r in rows)
            else 'inconclusive', 'originalFailurePreserved': True,
            'normalCloseAndCleanupAreSeparate': True}


