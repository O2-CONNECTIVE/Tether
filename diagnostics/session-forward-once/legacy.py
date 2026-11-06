"""Fixed-vocabulary legacy native evidence sanitizer; no launch or policy changes."""
import hashlib,json,math,re
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
             re.fullmatch(r'\d{1,8}(?:\.\d{1,8}){1,3}', value)  or
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
