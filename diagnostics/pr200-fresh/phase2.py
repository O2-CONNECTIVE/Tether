"""One fresh-runner observation only. No retry, build, executable upload or loop."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import re

import profile_contract
import report
import evidence_schema
import powershell_preflight
import owned_schema
import owned_overlay


def profile_safe(value):
    fields = {'schema', 'prelaunchAbsent', 'parentOwned', 'parentReparseFree', 'queryComplete',
              'lineageConfirmed', 'allObservedWebviewsCovered', 'rows', 'omittedRows'}
    row_fields = {'identityVerified', 'mapping', 'actualDirectoryExists', 'actualPathReparseFree',
                  'actualCanonicalPathMatches', 'requestedDirectoryCreated', 'requestedPathReparseFree'}
    report.need(isinstance(value, dict) and set(value) == fields and type(value['schema']) is int and value['schema'] == 1)
    report.need(all(type(value[key]) is bool for key in fields - {'schema', 'rows', 'omittedRows'}))
    report.need(type(value['omittedRows']) is int and 0 <= value['omittedRows'] <= 4096)
    report.need(isinstance(value['rows'], list) and len(value['rows']) <= 32)
    for row in value['rows']:
        report.need(isinstance(row, dict) and set(row) == row_fields)
        report.need(row['mapping'] in {'requested-root', 'requested-child-ebwebview', 'outside-requested-profile'})
        report.need(all(type(row[key]) is bool for key in row_fields - {'mapping'}))
    return value


def capability_safe(value):
    numeric = {'schema', 'requests', 'settlements'}
    flags = {'requestedMatchesEnvironment', 'requestedAbsentBeforeRequest', 'capabilityObserved',
             'capabilityMatchesRequested', 'captureFailed'}
    report.need(isinstance(value, dict) and set(value) == numeric | flags | {'capabilityMapping'})
    report.need(all(type(value[key]) is int and 0 <= value[key] <= 256 for key in numeric))
    report.need(all(type(value[key]) is bool for key in flags))
    report.need(value['capabilityMapping'] in {'requested-root', 'requested-child-ebwebview',
                                             'outside-requested-profile', 'unavailable'})
    return value


preflight_safe = powershell_preflight.preflight_safe
preflight = powershell_preflight.preflight

def decision(exits, reports, profile, capability, diagnostics=None):
    native, reason = report.verdict(*exits, reports)
    required = set(report.FILES) - {'driver-startup-log-facts.json'}
    if exits[0] != 0:
        required.remove('webview-result.json')
    complete = required <= set(reports)
    profile_ok = (native_profile_confirmed(reports.get('native-result.json', {})) and
                  profile_contract.capability_accepted(capability, profile))
    classified = evidence_schema.classification_complete(diagnostics, profile, capability)
    continuation = reason == 'continue' and complete and profile_ok and classified
    return {**native, 'evidenceComplete': complete, 'profileVerified': profile_ok,
            'classificationComplete': classified,
            'continue': continuation, 'pr200Blocked': True,
            'stopReason': 'single-observation-complete-inconclusive' if continuation else
                reason if reason != 'continue' else
                'classification-evidence-incomplete' if complete and profile_ok and not classified else
                'profile-or-required-evidence-unconfirmed',
            'interpretation': 'reproduced-new-specimen' if native['matchingTimeout'] else 'inconclusive'}


def native_profile_confirmed(native):
    """Prove only the unchanged supervisor's exact set; independent counts cannot."""
    facts = native.get('childCloseFacts', {})
    count = native.get('webviewChildrenObserved')
    if not isinstance(facts, dict) or type(count) is not int or count <= 0:
        return False
    rows = facts.get('rows')
    if not isinstance(rows, list) or facts.get('omittedRows') != 0 or facts.get('observed') != len(rows):
        return False
    webviews = [row for row in rows if isinstance(row, dict) and row.get('role') == 'webview']
    return len(webviews) == count and all(row.get('handleIdentityVerified') is True and
        row.get('creationKnown') is True and row.get('profileMatch') is True for row in webviews)


def identity_safe(value):
    required = {'schema', 'diagnosticOnly', 'distribution', 'sourceCommit', 'sourceTree', 'controlCommit',
                'features', 'profile', 'specimen', 'requestDeadlineMs', 'trialCap', 'originalFailure', 'hashes'}
    optional = {'inventory', 'driverProvenance', 'edgeDriverSha256', 'observedToolVersions'}
    report.need(isinstance(value, dict) and required <= set(value) <= required | optional)
    report.need(value['schema'] == 1 and value['diagnosticOnly'] is True and value['distribution'] is False)
    report.need(value['sourceCommit'] == report.SOURCE and value['sourceTree'] == report.TREE and
                re.fullmatch('[0-9a-f]{40}', value['controlCommit']))
    report.need(value['features'] == ['custom-protocol'] and value['profile'] == 'release' and
                value['specimen'] == 'new-build-not-original-binary' and value['requestDeadlineMs'] == 15000 and value['trialCap'] == report.CAP)
    report.need(value['originalFailure'] == {'run': 42037151371, 'job': 127278142941, 'attempt': 1})
    report.need(set(value['hashes']) == {'cargoLockSha256', 'packageLockSha256', 'toolPinsSha256',
                'supervisorSha256', 'builtExeSha256', 'exeSha256', 'archiveSha256'})
    report.need(all(x is None or isinstance(x, str) and re.fullmatch('[0-9a-f]{64}', x) for x in value['hashes'].values()))
    for key in ['inventory', 'driverProvenance']:
        if key in value:
            report.safe(value[key])
    if 'edgeDriverSha256' in value:
        report.need(re.fullmatch('[0-9a-f]{64}', value['edgeDriverSha256']))
    if 'observedToolVersions' in value:
        versions = value['observedToolVersions']
        report.need(set(versions) == {'node', 'python', 'rust'} and versions['node'] == 'v24.22.1' and
                    versions['python'] == 'Python 3.13.17' and re.fullmatch(r'rustc 1\.99\.1 \([0-9a-f]+ \d{4}-\d{2}-\d{2}\)', versions['rust']))
    return value


def trial_safe(value):
    numeric = {'trial', 'supervisorExit', 'inspectorExit', 'auditExit', 'observerExit', 'sessionElapsedMs'}
    flags = {'continue', 'pr200Blocked', 'matchingTimeout', 'sessionTimeoutSymptom', 'cleanupConfirmed',
             'normalCloseVerified', 'evidenceComplete', 'profileVerified', 'commandFailed', 'classificationComplete'}
    words = {'interpretation', 'stopReason'}
    report.need(isinstance(value, dict) and set(value) <= numeric | flags | words | {'rejectedEvidenceFiles'})
    report.need({'trial', 'continue', 'pr200Blocked', 'interpretation', 'stopReason'} <= set(value))
    report.need(type(value['trial']) is int and value['trial'] == 1 and value['pr200Blocked'] is True)
    report.need(value['continue'] is not True or value.get('classificationComplete') is True)
    for key, item in value.items():
        if key in numeric:
            report.need(item is None or type(item) is int and -1 <= item <= 1_000_000)
        elif key in flags:
            report.need(type(item) is bool)
        elif key == 'interpretation':
            report.need(item in {'inconclusive', 'reproduced-new-specimen'})
        elif key == 'stopReason':
            report.need(item in {'prelaunch-boundary-failure', 'single-observation-complete-inconclusive',
                'matching-session-timeout', 'cleanup-uncertain-or-failed', 'unexpected-failure',
                'profile-or-required-evidence-unconfirmed', 'evidence-observer-or-byte-integrity-failure',
                'wrapper-failure-or-prelaunch-unknown', 'observer-cleanup-uncertain', 'classification-evidence-incomplete'})
        elif key == 'rejectedEvidenceFiles':
            report.need(isinstance(item, list) and len(item) <= 12 and
                set(item) <= set(report.FILES) | {'profile-observation.json', 'profile-capability.json',
                    'observer-failure-sites.json', 'profile-capability-shape.json'})
    return value


def publish(root, index):
    # Summary/log output is free of artifact-storage use. Exact allowlist, no glob/raw data.
    report.need(index == 1)
    files = {}
    validators = {'build-identity.json': identity_safe, 'launch-build-identity.json': identity_safe,
                  'trial.json': trial_safe, 'profile-observation.json': profile_safe,
                  'profile-capability.json': capability_safe,
                  'powershell-preflight.json': preflight_safe,
                  'profile-capability-shape.json': evidence_schema.capability_shape_safe,
                  'observer-failure-sites.json': evidence_schema.observer_sites_safe,
                  'owned-collection.json': owned_schema.capture_safe,
                  'owned-overlay-identity.json': owned_overlay.manifest_safe,
                  'owned-capture-verdict.json': owned_schema.verdict_safe,
                  'owned-hook-preflight.json': owned_schema.preflight_safe}
    validators.update({'observation/' + name: report.safe for name in report.FILES})
    for name, validator in validators.items():
        file = root / 'safe' / name
        if file.exists():
            files[name] = validator(report.read(file))
    report.need('build-identity.json' in files)
    if 'trial.json' in files:
        report.need(files['trial.json']['trial'] == index)
        if files['trial.json']['continue']:
            report.need(powershell_preflight.current_passed(files.get('powershell-preflight.json', {})))
            report.need(evidence_schema.classification_complete(
                {name: files[name] for name in ['profile-capability-shape.json', 'observer-failure-sites.json'] if name in files},
                files.get('profile-observation.json', {}), files.get('profile-capability.json', {})))
    record = {'schema': 1, 'diagnosticOnly': True, 'trial': index,
              'files': files, 'fileSha256': {name: hashlib.sha256(json.dumps(value, sort_keys=True,
                  separators=(',', ':')).encode()).hexdigest() for name, value in files.items()}}
    raw = json.dumps(record, sort_keys=True, separators=(',', ':')).encode()
    report.need(len(raw) <= 300000)
    digest = hashlib.sha256(raw).hexdigest()
    rendered = json.dumps(record, sort_keys=True, indent=2)
    body = 'BEGIN_PR200_FRESH_RECORD sha256:' + digest + '\n' + rendered + '\nEND_PR200_FRESH_RECORD\n'
    report.need(len(body.encode()) <= 600000 and max(map(len, body.splitlines())) <= 8000)
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a', encoding='utf-8') as stream:
        stream.write('```json\n' + body + '```\n')
    print(body, end='', flush=True)
    return 0


def trial(source, control, root, index, *, supervisor_path=None, harness_path=None):
    report.need(sys.platform == 'win32' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted')
    report.need(index == 1)
    report.need(powershell_preflight.current_passed(report.read(root / 'safe/powershell-preflight.json')))
    build = report.identity(source, control, root, True)
    report.need(all(build['hashes'].values()) and build.get('driverProvenance'))
    report.need(build['hashes']['builtExeSha256'] == build['hashes']['exeSha256'])
    report.need(report.digest(root / 'tools/tauri/bin/tauri-driver.exe') == build['driverProvenance']['binarySha256'])
    pins = report.read(source / 'ui/tauri/native-smoke/tools.json')
    report.need(report.digest(root / 'tools/edge/msedgedriver.exe') == pins['driverExeSha256'])
    # Baseline is immutable even if the final identity collector observes later drift.
    report.write(root / 'safe/launch-build-identity.json', build)
    tools = root / 'tools'
    report.need(all(not (tools / name).exists() for name in ['webview-profile', 'driver-stage.jsonl',
                'driver-stage.finish', 'driver.stdout.log', 'driver.stderr.log', 'test.stdout.log', 'test.stderr.log']))
    private = root / 'single-trial'
    private.mkdir(exist_ok=False)
    output = private / 'output'
    native = source / 'ui/tauri/native-smoke'
    helper = control / 'diagnostics/pr200-fresh'
    ready, finish = private / 'observer.ready', private / 'observer.finish'
    profile_path = private / 'profile-observation.json'
    observer = None
    observer_exit = None
    command_failed = False
    result = {'trial': index, 'continue': False, 'pr200Blocked': True,
              'interpretation': 'inconclusive', 'stopReason': 'prelaunch-boundary-failure', 'classificationComplete': False}
    report.write(root / 'safe/trial.json', result)
    observer_log = (private / 'observer.log').open('wb')
    try:
        observer = subprocess.Popen(['pwsh', '-NoLogo', '-NoProfile', '-File', str(helper / 'observe-profile.ps1'),
            '-SourceDirectory', str(source), '-ToolsDirectory', str(tools), '-ApplicationPath', str(root / report.EXE),
            '-OutputPath', str(profile_path), '-ReadyPath', str(ready), '-FinishPath', str(finish)],
            stdout=observer_log, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 15
        while not ready.exists() and observer.poll() is None and time.monotonic() < deadline:
            time.sleep(0.05)
        report.need(ready.exists() and observer.poll() is None)
        env = os.environ.copy()
        report.need(not env.get('NODE_OPTIONS'))
        # Only the supervisor child inherits this observer preload; requests and code remain fixed.
        env['NODE_OPTIONS'] = '--import=' + (helper / 'capture-capability.mjs').as_uri()
        commands = [
            ('supervisor', 180, ['pwsh', '-NoLogo', '-NoProfile', '-File', str(supervisor_path or native / 'supervise.ps1'),
             '-ToolsDirectory', str(tools), '-OutputDirectory', str(output), '-ApplicationPath', str(root / report.EXE)]),
            ('inspector', 60, ['pwsh', '-NoLogo', '-NoProfile', '-File', str(native / 'inspect-driver-trace.ps1'),
             '-ToolsDirectory', str(tools), '-OutputDirectory', str(output)]),
            ('audit', 30, ['pwsh', '-NoLogo', '-NoProfile', '-File', str(helper / 'audit.ps1'),
             '-ApplicationPath', str(root / report.EXE), '-ToolsDirectory', str(tools),
             '-HarnessDirectory', str(harness_path or native), '-OutputPath', str(output / 'cleanup-audit.json')])]
        exits = []
        for label, timeout, args in commands:
            try:
                with (private / (label + '.log')).open('wb') as log:
                    exits.append(subprocess.run(args, env=env if label == 'supervisor' else None,
                        stdout=log, stderr=subprocess.STDOUT, timeout=timeout, check=False).returncode)
            except Exception:
                command_failed = True
                exits.append(-1)
            if label == 'supervisor':
                try:
                    finish.write_text('finish', encoding='ascii')
                except Exception:
                    command_failed = True
        try:
            observer_exit = observer.wait(timeout=10)
        except subprocess.TimeoutExpired:
            command_failed = True
            observer.kill()  # Popen-owned observer only; no name/path/snapshot kill.
            observer_exit = observer.wait(timeout=5)
        reports = {}
        rejected = []
        for name in report.FILES:
            if (output / name).exists():
                try:
                    value = report.safe(report.read(output / name))
                    report.write(root / 'safe/observation' / name, value)
                    reports[name] = value
                except Exception:
                    rejected.append(name)
        profile = {}
        capability = {}
        try:
            profile = profile_safe(report.read(profile_path))
            # The native collection itself is authoritative. Same cardinality across
            # independent snapshots is never used as evidence of identity equality.
            profile['allObservedWebviewsCovered'] = native_profile_confirmed(reports.get('native-result.json', {}))
            report.write(root / 'safe/profile-observation.json', profile)
        except Exception:
            rejected.append('profile-observation.json')
        try:
            capability = capability_safe(report.read(output / 'profile-capability.json'))
            report.write(root / 'safe/profile-capability.json', capability)
        except Exception:
            rejected.append('profile-capability.json')
        diagnostics = {}
        for name, file, validator in [
            ('profile-capability-shape.json', output / 'profile-capability-shape.json', evidence_schema.capability_shape_safe),
            ('observer-failure-sites.json', private / 'observer-failure-sites.json', evidence_schema.observer_sites_safe)]:
            try:
                diagnostics[name] = validator(report.read(file))
                report.write(root / 'safe' / name, diagnostics[name])
            except Exception:
                rejected.append(name)
        result = {'trial': index, **decision(exits, reports, profile, capability, diagnostics),
                  'observerExit': observer_exit, 'rejectedEvidenceFiles': rejected,
                  'commandFailed': command_failed}
        stable = report.digest(root / report.EXE) == build['hashes']['exeSha256'] and report.digest(root / report.ARCHIVE) == build['hashes']['archiveSha256']
        if rejected or command_failed or observer_exit != 0 or not stable:
            result['continue'] = False
            result['stopReason'] = 'evidence-observer-or-byte-integrity-failure'
    except Exception:
        result['continue'] = False
        result['stopReason'] = 'wrapper-failure-or-prelaunch-unknown'
    finally:
        try:
            finish.write_text('finish', encoding='ascii')
        except Exception:
            result['continue'] = False
            result['stopReason'] = 'observer-cleanup-uncertain'
        if observer is not None and observer.poll() is None:
            try:
                observer.wait(timeout=10)
            except subprocess.TimeoutExpired:
                observer.kill()
                observer.wait(timeout=5)
                result['continue'] = False
                result['stopReason'] = 'observer-cleanup-uncertain'
        observer_log.close()
        if profile_path.exists() and not (root / 'safe/profile-observation.json').exists():
            try:
                report.write(root / 'safe/profile-observation.json', profile_safe(report.read(profile_path)))
            except Exception:
                result['continue'] = False
        for name, file, validator in [
            ('profile-capability-shape.json', output / 'profile-capability-shape.json', evidence_schema.capability_shape_safe),
            ('observer-failure-sites.json', private / 'observer-failure-sites.json', evidence_schema.observer_sites_safe)]:
            if file.exists() and not (root / 'safe' / name).exists():
                try:
                    report.write(root / 'safe' / name, validator(report.read(file)))
                except Exception:
                    result['continue'] = False
                    result['classificationComplete'] = False
        report.write(root / 'safe/trial.json', result)
        # Job output is emitted only after cleanup, observation and evidence validation.
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as stream:
            stream.write('continue=' + ('true' if result['continue'] else 'false') + '\n')
    print('FRESH_RUNNER_OBSERVATION_COMPLETE' if result['continue'] else 'FRESH_RUNNER_STOPPED', flush=True)
    return 0 if result['continue'] else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('stage', choices=['identity', 'tools', 'preflight', 'trial', 'publish'])
    for name in ['source', 'control', 'root']:
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--trial', type=int, choices=[1])
    args = parser.parse_args()
    try:
        paths = (args.source.resolve(), args.control.resolve(), args.root.resolve())
        if args.stage == 'trial':
            return trial(*paths, args.trial)
        if args.stage == 'preflight':
            return preflight(paths[1], paths[2])
        if args.stage == 'publish':
            return publish(paths[2], args.trial)
        report.identity(*paths, args.stage == 'tools')
        print('FRESH_RUNNER_IDENTITY_RECORDED')
        return 0
    except Exception:
        print('FRESH_RUNNER_BOUNDARY_REJECTED')
        return 1


if __name__ == '__main__':
    sys.exit(main())
