"""Two original launches, one setup. External control only; no cleanup authority."""
import contextlib
import io
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import time
import bounded_pump_capture as control

SETUP = ('unpacked/BUILD.json', 'overlay.json')
TRIAL = ('steps.json', 'evidence/native-result.json', 'evidence/native-observation.json',
         'tools/app-startup/wry-completion-probe.json', 'tools/app-startup/wry-sdk-delivery.json',
         'tools/app-startup/wry-receiver-polls.json', 'tools/app-startup/wry-pump-brackets.json',
         'tools/driver-stage.jsonl', 'evidence/app-startup-report.json', 'launch.json', 'final.json')
TOOLS = ('edge/msedgedriver.exe', 'tauri/bin/tauri-driver.exe', 'driver-provenance.json', 'tauri-driver.crate')


class PublicationView:
    """Closed routing, not a second specimen or a claim that setup ran again."""
    def __init__(self, setup, trial):
        self.setup, self.trial = setup, trial

    def __truediv__(self, name):
        if name in SETUP: return self.setup / name
        if name in TRIAL: return self.trial / name
        raise ValueError('UNEXPECTED_PUBLICATION_PATH')


def serial(invoke):
    for ordinal in (1, 2):
        try: outcome = invoke(ordinal)
        except Exception: return 'unknown'
        if outcome != 'healthy': return outcome
    return 'mechanical-reuse-demonstrated'


def regular(path):
    info = path.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or
            getattr(info, 'st_file_attributes', 0) & 1024): raise ValueError('INPUT')
    for parent in path.parents:
        info = parent.lstat()
        if not stat.S_ISDIR(info.st_mode) or getattr(info, 'st_file_attributes', 0) & 1024:
            raise ValueError('INPUT')
    return path.read_bytes()


def hashes(paths, source):
    return {name: source.digest(regular(path)) for name, path in paths.items()}


def immutable_tools(setup, root):
    # The explicit source whitelist is derived only from checksum-pinned crate/patch.
    from apply_driver_trace import upstream_files, apply_exact, bundle, check_tree
    base = setup / 'tools'
    patch, code, _ = bundle(root / 'tools/native-driver-trace')
    after = apply_exact(upstream_files(regular(base / 'tauri-driver.crate')), patch)
    after.update({'src/' + name: data for name, data in code.items()})
    check_tree(base / 'tauri-driver-2.1.0', after)
    return (*TOOLS, *('tauri-driver-2.1.0/' + name for name in sorted(after)))


def stage(setup, trial, names, source):
    expected = hashes({name: setup / 'tools' / name for name in names}, source)
    trial.mkdir()  # Never reuse, merge, overwrite, or copy mutable state.
    for name in names:
        destination = trial / 'tools' / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        with destination.open('xb') as stream: stream.write(regular(setup / 'tools' / name))
    actual = {p.relative_to(trial / 'tools').as_posix(): p for p in (trial / 'tools').rglob('*') if p.is_file()}
    if set(actual) != set(names) or hashes(actual, source) != expected: raise ValueError('STAGING')
    return expected


def identity(root, setup, trial, source):
    source.permissions(root, True)
    value = source.source_identity(root, setup / 'wry', setup / 'wry.crate', True)
    if value['sourceCommit'] != control.SOURCE or value['sourceTree'] != control.TREE: raise ValueError('PIN')
    for name in ('rescue-host-shell.exe', 'BUILD.json'):
        if regular(setup / 'unpacked' / name) != regular(setup / 'fixture' / name): raise ValueError('SPECIMEN')
    paths = {'exeSha256': setup / 'unpacked/rescue-host-shell.exe',
             'driverSha256': trial / 'tools/tauri/bin/tauri-driver.exe',
             'archiveSha256': setup / 'fixture.zip',
             'edgeDriverSha256': trial / 'tools/edge/msedgedriver.exe',
             'driverPatchSha256': root / 'tools/native-driver-trace/driver.patch',
             'driverLockSha256': trial / 'tools/tauri-driver-2.1.0/Cargo.lock'}
    value.update(hashes(paths, source))
    return source.safe_meta(value, delivery=True)


def run(arguments, cwd, output=None):
    try: result = subprocess.run(arguments, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    except OSError: return 65535
    if output is not None:
        with output.open('xb') as stream: stream.write(result.stdout)
    return result.returncode if 0 <= result.returncode <= 65535 else 65535


def snapshot(expected, cwd):
    # Capture in memory only. Never print or retain the raw process query.
    from two_launch_readiness import snapshot_summary
    result = subprocess.run(['pwsh', '-NoLogo', '-NoProfile', '-File',
                             str(Path(__file__).with_name('two-launch-snapshot.ps1'))], cwd=cwd,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    if result.returncode != 0 or len(result.stdout) > 2**20: raise ValueError('SNAPSHOT')
    value = json.loads(result.stdout.decode('utf-8-sig'))
    return snapshot_summary(value['rows'], value['ports'], expected)


def publish(root, setup, trial, source):
    output = io.StringIO()
    passed = False
    try:
        with contextlib.redirect_stdout(output): passed = source.publish(root, PublicationView(setup, trial), True)
    except Exception: pass
    text = output.getvalue()
    lines = text.splitlines()
    if len(text.encode()) > 34000 or len(lines) != 4: raise ValueError('PUBLICATION')
    body = json.loads(lines[1])
    if (lines[0] != 'WRY_DIAGNOSTIC_RECORD_BEGIN' or lines[3] != 'WRY_DIAGNOSTIC_RECORD_END'
            or control.canonical(body).decode() != lines[1]
            or lines[2] != 'WRY_DIAGNOSTIC_RECORD_SHA256=' + control.digest(body)): raise ValueError('PUBLICATION')
    print(text, end='')
    return passed is True, body


def usable_failure_observation(value):
    """A canonical unknown/null projection is still unavailable evidence."""
    from two_launch_readiness import integer
    keys = {'status', 'phase', 'elapsedMs', 'documentObservations', 'droppedRequests',
            'errorClass', 'sessionRequests'}
    phases = {'bootstrap', 'session-create', 'origin-check', 'readiness', 'main-screenshot',
              'unwired-contract', 'keyboard', 'diagnostics-screenshot', 'result-write', 'complete'}
    if (type(value) is not dict or set(value) != keys or value['status'] != 'failed'
            or type(value['phase']) is not str or value['phase'] not in phases
            or value['errorClass'] not in ('timeout', 'other')
            or any(not integer(value[k], 0, 2**53-1) for k in ('elapsedMs', 'documentObservations'))
            or not integer(value['droppedRequests'], 0, 0)
            or type(value['sessionRequests']) is not list or len(value['sessionRequests']) > 256):
        return False
    for row in value['sessionRequests']:
        if (type(row) is not dict or set(row) != {'outcome', 'elapsedMs', 'httpStatus'}
                or row['outcome'] not in ('started', 'ok', 'timeout', 'transport', 'http', 'webdriver', 'invalid-response')
                or not integer(row['elapsedMs'], 0, 2**53-1)
                or (row['httpStatus'] is not None and not integer(row['httpStatus'], 100, 599))):
            return False
    return True


def failure_classification(raw, projection, body, final_ok, launch, native_exit):
    """Keep the observed symptom distinct from a source/specimen-bound target red."""
    from two_launch_readiness import integer
    observed = False
    bound = False
    try:
        requests = projection['sessionRequests']
        usable = usable_failure_observation(projection)
        observed = (usable and integer(native_exit, 1, 65535)
                    and integer(raw.get('nativeTestsRun'), 1, 1)
                    and integer(raw.get('nativeTestsPassed'), 0, 0)
                    and projection['status'] == 'failed' and projection['phase'] == 'session-create' and projection['errorClass'] == 'timeout'
                    and integer(projection['documentObservations'], 0, 0) and len(requests) == 2
                    and requests[0]['outcome'] == 'started' and requests[1]['outcome'] == 'timeout'
                    and integer(requests[1]['elapsedMs'], 15000, 2**53-1))
        files = body['files']
        data = lambda name: files[name]['data']
        bound = (usable and integer(native_exit, 1, 65535)
                 and integer(raw.get('nativeTestsRun'), 1, 1)
                 and integer(raw.get('nativeTestsPassed'), 0, 0)
                 and final_ok is True and body['sourceAndSpecimenStable'] is True
                 and body['sourceCommit'] == control.SOURCE and body['sourceTree'] == control.TREE
                 and body['controlCommit'] == os.environ.get('CONTROL_HEAD')
                 and launch['sourceCommit'] == control.SOURCE and launch['sourceTree'] == control.TREE
                 and control.canonical(data('launch')) == control.canonical(launch)
                 and control.canonical(data('final')) == control.canonical(launch)
                 and control.canonical(data('observation')) == control.canonical(projection)
                 and integer(data('native').get('nativeTestsRun'), 1, 1)
                 and integer(data('native').get('nativeTestsPassed'), 0, 0)
                 and data('build')['sourceCommit'] == control.SOURCE
                 and data('build')['exeSha256'] == launch['exeSha256']
                 and data('overlay')['overlayLockSha256'] == launch['overlayLockSha256'])
    except (KeyError, TypeError, ValueError): pass
    return {'observedSessionTimeout': observed, 'targetRedBound': observed and bound,
            'outcome': 'not-applicable' if integer(native_exit, 0, 0) else
                       'target-red' if observed and bound else 'other-native-failure' if bound
                       else 'failure-evidence-inconclusive'}


def execute(root, setup, source, cwd):
    from two_launch_readiness import eligibility
    control.admit(setup, source)
    if any(name.upper().startswith('WEBVIEW2_') for name in os.environ): raise ValueError('ENV_OVERRIDE')
    began = source.load(setup / 'setup-start.json', 64)
    setup_ms = round(time.time() * 1000) - began if type(began) is int else -1
    if not 0 <= setup_ms <= 35 * 60 * 1000: raise ValueError('SETUP_TIME')
    setup_steps = source.safe_steps(source.load(setup / 'steps.json', 4096))
    names = immutable_tools(setup, root)
    stable = {'app': setup / 'unpacked/rescue-host-shell.exe', 'manifest': setup / 'unpacked/BUILD.json',
              'overlay': setup / 'overlay.json', 'archive': setup / 'fixture.zip'}
    # Every tracked source byte is covered by source_identity; explicitly bind launch harness too.
    stable.update({name: root / 'ui/tauri/native-smoke' / name for name in
                   ('supervise.ps1', 'run.ts', 'webdriver.ts', 'close-observation.ps1')})
    baseline = hashes(stable, source)
    tool_hashes = hashes({name: setup / 'tools' / name for name in names}, source)
    receipt = {'schema': 1, 'setupExecutions': 1, 'sameVm': True, 'maxNativeInvocations': 2,
               'setupElapsedMs': setup_ms, 'setupStages': setup_steps, 'immutableSha256': control.digest(baseline),
               'toolSetSha256': control.digest(tool_hashes), 'qualification': control.receipt(setup),
               'rootCauseEstablished': False, 'timeoutFixed': False}
    control.write_new(setup / 'two-launch-setup.json', receipt)
    control.emit('TWO_LAUNCH_SETUP', receipt)

    previous = []

    def invoke(ordinal):
        started = time.monotonic()
        trial = setup / ('trial-' + str(ordinal))
        staged = stage(setup, trial, names, source)
        if staged != tool_hashes or hashes(stable, source) != baseline: raise ValueError('DRIFT')
        launch = identity(root, setup, trial, source)
        if launch != source.load(setup / 'launch.json', 4096): raise ValueError('DRIFT')
        control.write_new(trial / 'launch.json', launch)
        expected = [str(stable['app']), str(trial / 'tools/tauri/bin/tauri-driver.exe'),
                    str(trial / 'tools/edge/msedgedriver.exe')]
        if previous and snapshot(previous, cwd).get('eligible') is not True:
            raise ValueError('PRIOR_TRIAL_READINESS')
        before = snapshot(expected, cwd)
        if before.get('eligible') is not True: raise ValueError('PRELAUNCH_READINESS')
        stages = []
        previous[:] = expected
        native_started = time.monotonic()
        native_exit = run(['pwsh', '-NoLogo', '-NoProfile', '-File', str(root / 'ui/tauri/native-smoke/supervise.ps1'),
                          '-ToolsDirectory', str(trial / 'tools'), '-OutputDirectory', str(trial / 'evidence'),
                          '-ApplicationPath', str(stable['app']), '-StartupDiagnostic',
                          '-StartupManifest', str(stable['manifest']), '-StartupSource', control.SOURCE], cwd)
        native_ms = round((time.monotonic() - native_started) * 1000)
        stages.append({'stage': 'native', 'exitCode': native_exit})
        (trial / 'evidence').mkdir(exist_ok=True)
        # Preserve reports even on native failure; never add cleanup beyond frozen supervisor.
        stages.append({'stage': 'host-report', 'exitCode': run(['node', str(root / 'ui/tauri/native-smoke/inspect-app-startup.ts'),
                       '--directory', str(trial / 'tools/app-startup'), '--manifest', str(stable['manifest']),
                       '--exe', str(stable['app']), '--source', control.SOURCE,
                       '--collection', str(trial / 'evidence/native-result.json')], cwd,
                       trial / 'evidence/app-startup-report.json')})
        stages.append({'stage': 'driver-report', 'exitCode': run(['pwsh', '-NoLogo', '-NoProfile', '-File',
                       str(root / 'ui/tauri/native-smoke/inspect-driver-trace.ps1'), '-ToolsDirectory',
                       str(trial / 'tools'), '-OutputDirectory', str(trial / 'evidence')], cwd)})
        final_ok = False
        try:
            final = identity(root, setup, trial, source)
            final_ok = (final == launch and hashes(stable, source) == baseline and
                        hashes({name: trial / 'tools' / name for name in names}, source) == staged and
                        hashes({name: setup / 'tools' / name for name in names}, source) == tool_hashes)
            control.write_new(trial / 'final.json', final)
        except Exception: pass
        stages.append({'stage': 'final-identity', 'exitCode': 0 if final_ok else 1})
        control.write_new(trial / 'steps.json', setup_steps + stages)
        try: passed, body = publish(root, setup, trial, source)
        except Exception: passed, body = False, None
        gate_started = time.monotonic()
        readiness = eligibility({}, 65535, {})
        raw_hash = None
        raw = {}
        try:
            raw = source.load(trial / 'evidence/native-result.json')
            raw_hash = source.digest(regular(trial / 'evidence/native-result.json'))
            readiness = eligibility(raw, native_exit, snapshot(expected, cwd))
        except Exception: pass
        healthy = passed and final_ok and readiness.get('eligible') is True
        projection = {}
        try: projection = source.observation_summary(source.load(trial / 'evidence/native-observation.json'))
        except Exception: pass
        failure = failure_classification(raw, projection, body, final_ok, launch, native_exit)
        result = {'schema': 1, 'trialOrdinal': ordinal, 'sameVm': True, 'reusedSpecimen': True,
                  'setupReceiptSha256': control.digest(receipt), 'reusedSetupStages': setup_steps,
                  'executedTrialStages': stages, 'diagnosticSha256': control.digest(body) if body is not None else None,
                  'gateElapsedMs': round((time.monotonic() - gate_started) * 1000), 'nativeResultSha256': raw_hash, 'readiness': readiness, 'nativeElapsedMs': native_ms,
                  'trialElapsedMs': round((time.monotonic() - started) * 1000),
                  'healthy': healthy, 'failureObservation': failure, 'rootCauseEstablished': False, 'timeoutFixed': False}
        control.write_new(trial / 'receipt.json', result)
        control.emit('TWO_LAUNCH_TRIAL', result)
        if healthy: return 'healthy'
        if native_exit == 0: return 'readiness-or-evidence-inconclusive'
        return failure['outcome']
    outcome = serial(invoke)
    control.emit('TWO_LAUNCH_FINAL', {'outcome': outcome, 'rootCauseEstablished': False, 'timeoutFixed': False})
    return 0 if outcome == 'mechanical-reuse-demonstrated' else 1


def main():
    try:
        parser = control.ClosedParser(add_help=False)
        parser.add_argument('--root', type=Path, required=True)
        parser.add_argument('--temp', type=Path, required=True)
        args = parser.parse_args()
        if sys.platform != 'win32': raise ValueError('WINDOWS_REQUIRED')
        if os.environ.get('TRIAL_ORDINAL') != '1': raise ValueError('BINDING')
        control.binding()
        source = control.source_module(args.root)
        return execute(args.root.resolve(), args.temp.resolve(), source, Path.cwd())
    except Exception:
        print('{"twoLaunch":"unknown-stop","rootCauseEstablished":false,"timeoutFixed":false}')
        return 1


if __name__ == '__main__': raise SystemExit(main())
