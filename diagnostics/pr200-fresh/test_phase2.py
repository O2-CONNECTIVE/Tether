"""Decision/publication fixtures only, never a Windows or WebView launch."""
import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import phase2
import profile_contract
import report
from test_profile_contract import valid


def capability(mapping='requested-root'):
    return {'schema': 1, 'requests': 1, 'settlements': 1, 'requestedMatchesEnvironment': True,
            'requestedAbsentBeforeRequest': True, 'capabilityObserved': True,
            'capabilityMatchesRequested': mapping == 'requested-root',
            'capabilityMapping': mapping, 'captureFailed': False}


def synthetic_classification():
    return {'profile-capability-shape.json': {'schema': 2, 'requests': 1, 'settlements': 1,
        'envelopeShape': 'object', 'valueShape': 'object', 'capabilitiesShape': 'object',
        'flatShape': 'missing', 'nestedContainerShape': 'object', 'nestedShape': 'string',
        'candidateClass': 'candidate-valid-nested', 'pathPredicate': 'accepted', 'failureSites': []},
        'observer-failure-sites.json': {'schema': 2, 'failureCount': 0, 'sites': [],
                                      'saturated': False, 'truncated': False, 'details': []}}


def synthetic_preflight():
    return {'schema': 2, 'kind': 'predicate-powershell-preflight', 'parseFilesExpected': 5,
            'parseFilesVerified': 5, 'fixturesExpected': 2, 'fixturesVerified': 2, 'passed': True,
            'stages': {key: {'runtimeExecuted': True, 'exitCode': 0, 'passed': True, 'code': 'pass'}
                       for key in ('parse', 'ledger', 'detail')}}


def reports():
    result = {'nativeTestsRun': 1, 'nativeTestsPassed': 1, 'normalCloseVerified': True,
        'cleanupStarted': True, 'cleanupFailures': 0, 'childHandleDisposeFailures': 0,
        'driverExitObservedAfterCleanup': True, 'webviewChildrenObserved': 1,
        'childCloseFacts': {'observed': 1, 'omittedRows': 0, 'rows': [{'role': 'webview',
            'handleIdentityVerified': True, 'creationKnown': True, 'profileMatch': True}]},
        'afterDriverCleanupFacts': {'observed': 1,
            'remainingSnapshot': 0, 'omittedRows': 0, 'rows': [
                {'handleIdentityVerified': True, 'handleState': 'exited', 'snapshotState': 'absent'}]}}
    observation = {'status': 'webview-passed-awaiting-close', 'requests': [
        {'operation': 'create-session', 'outcome': 'ok', 'elapsedMs': 6203}]}
    audit = {'queryComplete': True, **{key: 0 for key in ['appRemaining', 'driversRemaining',
        'profileProcessesRemaining', 'harnessNodesRemaining', 'listenersRemaining']}}
    return {'native-result.json': result, 'native-observation.json': observation,
        'cleanup-audit.json': audit, 'driver-stage-report.json': {'code': 'TRACE_COMPLETE',
            'requestStage': 'native-headers', 'observedStages': ['native-headers']},
        'driver-provenance.json': {'binarySha256': 'a' * 64},
        'session-startup-timing.json': {'schema': 1, 'outcome': 'success', 'settledMs': 6203, 'samples': []},
        'webview-result.json': {'actualRustIpc': True}}


def identity():
    return {'schema': 1, 'diagnosticOnly': True, 'distribution': False, 'sourceCommit': report.SOURCE,
        'sourceTree': report.TREE, 'controlCommit': 'a' * 40, 'features': ['custom-protocol'],
        'profile': 'release', 'specimen': 'new-build-not-original-binary', 'requestDeadlineMs': 15000,
        'trialCap': report.CAP, 'originalFailure': {'run': 42037151371, 'job': 127278142941, 'attempt': 1},
        'hashes': {key: None for key in ['cargoLockSha256', 'packageLockSha256', 'toolPinsSha256',
            'supervisorSha256', 'builtExeSha256', 'exeSha256', 'archiveSha256']}}


class Phase2Tests(unittest.TestCase):
    def test_native_success_needs_all_profile_and_required_report_gates(self):
        data = reports()
        self.assertTrue(phase2.decision([0, 0, 0], data, valid(), capability(), synthetic_classification())['continue'])
        self.assertFalse(phase2.decision([0, 0, 0], data, valid(), capability('requested-child-ebwebview'), synthetic_classification())['continue'])
        for mutate in [lambda p: p.update(queryComplete=False), lambda p: p.update(allObservedWebviewsCovered=False),
                       lambda p: p['rows'][0].update(mapping='outside-requested-profile'),
                       lambda p: p['rows'][0].update(actualPathReparseFree=False)]:
            profile = valid(); mutate(profile)
            self.assertFalse(phase2.decision([0, 0, 0], data, profile, capability(), synthetic_classification())['continue'])
        for name in data:
            missing = dict(data); del missing[name]
            self.assertFalse(phase2.decision([0, 0, 0], missing, valid(), capability(), synthetic_classification())['continue'])

    def test_capability_and_process_mapping_cannot_contradict(self):
        for change in [{'requests': 2}, {'settlements': 0}, {'capabilityObserved': False},
                       {'capabilityMapping': 'outside-requested-profile'}, {'captureFailed': True},
                       {'requestedAbsentBeforeRequest': False}]:
            value = capability(); value.update(change)
            self.assertFalse(profile_contract.capability_accepted(value, valid()))
        profile = valid(); profile['rows'][0]['mapping'] = 'requested-root'
        self.assertFalse(profile_contract.capability_accepted(capability('requested-child-ebwebview'), profile))

    def test_native_exact_collection_cannot_be_substituted_by_equal_observer_count(self):
        for key, value in [('profileMatch', False), ('profileMatch', None), ('handleIdentityVerified', False), ('creationKnown', False)]:
            data = reports(); data['native-result.json']['childCloseFacts']['rows'][0][key] = value
            self.assertFalse(phase2.decision([0, 0, 0], data, valid(), capability(), synthetic_classification())['continue'])
        data = reports(); data['native-result.json']['childCloseFacts']['omittedRows'] = 1
        self.assertFalse(phase2.decision([0, 0, 0], data, valid(), capability(), synthetic_classification())['continue'])
        data = reports(); del data['native-result.json']['childCloseFacts']
        self.assertFalse(phase2.decision([0, 0, 0], data, valid(), capability(), synthetic_classification())['continue'])
        data = reports(); data['native-result.json']['childCloseFacts']['rows'][0]['profileMatch'] = False
        profile = valid(); profile['rows'][0]['mapping'] = 'requested-child-ebwebview'
        self.assertFalse(phase2.decision([0, 0, 0], data, profile, capability('requested-child-ebwebview'), synthetic_classification())['continue'])

    def test_cleanup_uncertainty_and_original_timeout_stay_red(self):
        data = reports(); data['cleanup-audit.json']['appRemaining'] = None
        self.assertEqual(phase2.decision([0, 0, 0], data, valid(), capability(), synthetic_classification())['stopReason'], 'cleanup-uncertain-or-failed')
        data = reports()
        data['native-observation.json'].update(status='failed', phase='session-create',
            errorCode='NATIVE_DRIVER_TIMEOUT', documentObservations=0, originClass='unobserved',
            titleClass='unobserved', lastRequest={'operation': 'create-session', 'outcome': 'timeout', 'elapsedMs': 15017})
        data['driver-stage-report.json'].update(requestStage='dropped-before-headers',
            observedStages=['session-received', 'forward-start', 'request-dropped'])
        verdict = phase2.decision([1, 0, 0], data, {}, {}, synthetic_classification())
        self.assertEqual(verdict['stopReason'], 'matching-session-timeout')
        self.assertTrue(verdict['matchingTimeout'])
        self.assertFalse(verdict['continue'])
        self.assertTrue(verdict['pr200Blocked'])

    def test_summary_and_log_preserve_complete_safe_json_with_verifiable_hash(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            report.write(root / 'safe/build-identity.json', identity())
            for name, value in reports().items():
                report.write(root / 'safe/observation' / name, value)
            report.write(root / 'safe/profile-observation.json', valid())
            report.write(root / 'safe/profile-capability.json', capability())
            report.write(root / 'safe/powershell-preflight.json', synthetic_preflight())
            for name, value in synthetic_classification().items(): report.write(root / 'safe' / name, value)
            report.write(root / 'safe/trial.json', {'trial': 1, **phase2.decision([0, 0, 0], reports(), valid(), capability(), synthetic_classification())})
            summary = root / 'summary.md'; output = io.StringIO()
            with patch.dict(os.environ, GITHUB_STEP_SUMMARY=str(summary)), contextlib.redirect_stdout(output):
                self.assertEqual(phase2.publish(root, 1), 0)
            lines = output.getvalue().splitlines()
            record = json.loads('\n'.join(lines[1:-1]))
            digest = hashlib.sha256(json.dumps(record, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
            self.assertEqual(lines[0], 'BEGIN_PR200_FRESH_RECORD sha256:' + digest)
            self.assertEqual(lines[-1], 'END_PR200_FRESH_RECORD')
            self.assertEqual(len(record['files']), 14)
            self.assertIn(output.getvalue(), summary.read_text())
            self.assertNotIn(str(root), output.getvalue())
            self.assertLess(len(summary.read_bytes()), 600000)
            # An unknown field must reject the entire publication; never print raw contents.
            report.write(root / 'safe/profile-capability.json', {**capability(), 'path': 'PRIVATE_MARKER'})
            output = io.StringIO()
            with contextlib.redirect_stdout(output), self.assertRaises(ValueError):
                phase2.publish(root, 1)
            self.assertEqual(output.getvalue(), '')

    def test_publication_size_limit_rejects_before_any_record_is_printed(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            report.write(root / 'safe/build-identity.json', identity())
            large = {'requests': [{'errorCode': 'NATIVE_' + 'X' * 75} for _ in range(512)]}
            for name in report.FILES:
                report.write(root / 'safe/observation' / name, large)
            output = io.StringIO()
            with contextlib.redirect_stdout(output), self.assertRaises(ValueError):
                phase2.publish(root, 1)
            self.assertEqual(output.getvalue(), '')

    def test_finish_marker_failure_still_inspects_audits_salvages_and_stops(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder); source = root / 'source'; control = root / 'control'
            report.write(source / 'ui/tauri/native-smoke/tools.json', {'driverExeSha256': 'a' * 64})
            report.write(root / 'safe/powershell-preflight.json', synthetic_preflight())
            (root / 'tools').mkdir()
            build = {'hashes': {key: 'a' * 64 for key in ['exeSha256', 'archiveSha256', 'builtExeSha256']},
                     'driverProvenance': {'binarySha256': 'a' * 64}}
            calls = []
            original_write = Path.write_text

            class Observer:
                def __init__(self, args, **kwargs):
                    self.code = None
                    original_write(Path(args[args.index('-ReadyPath') + 1]), 'ready')
                    report.write(Path(args[args.index('-OutputPath') + 1]), valid())
                    report.write(Path(args[args.index('-OutputPath') + 1]).parent / 'observer-failure-sites.json',
                                 synthetic_classification()['observer-failure-sites.json'])
                def poll(self): return self.code
                def wait(self, **kwargs): self.code = 0; return 0
                def kill(self): self.code = -1

            def command(args, **kwargs):
                script = next(x for x in args if x.endswith('.ps1'))
                calls.append(Path(script).name)
                if script.endswith('supervise.ps1'):
                    output = Path(args[args.index('-OutputDirectory') + 1])
                    for name, value in reports().items(): report.write(output / name, value)
                    report.write(output / 'profile-capability.json', capability())
                    report.write(output / 'profile-capability-shape.json', synthetic_classification()['profile-capability-shape.json'])
                return SimpleNamespace(returncode=0)

            def write(path, *args, **kwargs):
                if path.name == 'observer.finish': raise OSError('PRIVATE_MARKER_FAILURE')
                return original_write(path, *args, **kwargs)

            with patch.object(phase2.sys, 'platform', 'win32'), \
                 patch.dict(os.environ, RUNNER_ENVIRONMENT='github-hosted', GITHUB_OUTPUT=str(root / 'outputs'), NODE_OPTIONS=''), \
                 patch.object(report, 'identity', return_value=build), patch.object(report, 'digest', return_value='a' * 64), \
                 patch.object(phase2.subprocess, 'Popen', Observer), patch.object(phase2.subprocess, 'run', side_effect=command), \
                 patch.object(Path, 'write_text', write):
                self.assertEqual(phase2.trial(source, control, root, 1), 1)
            self.assertEqual(calls, ['supervise.ps1', 'inspect-driver-trace.ps1', 'audit.ps1'])
            self.assertTrue((root / 'safe/observation/native-result.json').exists())
            self.assertTrue((root / 'safe/observation/cleanup-audit.json').exists())
            result = report.read(root / 'safe/trial.json')
            self.assertFalse(result['continue'])
            self.assertTrue(result['commandFailed'])
            self.assertNotIn('PRIVATE_MARKER_FAILURE', json.dumps(result))


if __name__ == '__main__':
    unittest.main()
