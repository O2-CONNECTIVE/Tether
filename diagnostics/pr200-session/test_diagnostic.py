"""Offline controller/sanitization fixtures; these never execute Windows or native code."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('diagnostic', Path(__file__).with_name('diagnostic.py'))
d = importlib.util.module_from_spec(spec)
spec.loader.exec_module(d)


def reports(failure=False):
    result = {'nativeTestsRun': 1, 'nativeTestsPassed': 0 if failure else 1,
              'normalCloseVerified': not failure, 'cleanupStarted': True, 'cleanupFailures': 0,
              'childHandleDisposeFailures': 0, 'driverExitObservedAfterCleanup': True,
              'afterDriverCleanupFacts': {'observed': 1, 'remainingSnapshot': 0, 'omittedRows': 0,
                  'rows': [{'handleIdentityVerified': True, 'handleState': 'exited', 'snapshotState': 'absent'}]}}
    obs = {'status': 'failed' if failure else 'webview-passed-awaiting-close',
           'phase': 'session-create' if failure else 'complete',
           'errorCode': 'NATIVE_DRIVER_TIMEOUT' if failure else '', 'documentObservations': 0 if failure else 1,
           'originClass': 'unobserved' if failure else 'bundled', 'titleClass': 'unobserved' if failure else 'unwired',
           'lastRequest': {'operation': 'create-session', 'outcome': 'timeout' if failure else 'ok',
                           'elapsedMs': 15017 if failure else 6203}}
    trace = {'code': 'TRACE_COMPLETE', 'requestStage': 'dropped-before-headers' if failure else 'native-headers',
             'observedStages': ['session-received', 'forward-start', 'request-dropped' if failure else 'native-headers']}
    audit = dict(queryComplete=True, **{key: 0 for key in ['appRemaining', 'driversRemaining',
            'profileProcessesRemaining', 'harnessNodesRemaining', 'listenersRemaining']})
    return {'native-result.json': result, 'native-observation.json': obs,
            'driver-stage-report.json': trace, 'cleanup-audit.json': audit,
            'session-startup-timing.json': {'schema': 1, 'purpose': 'diagnostic-hints-only',
                'settledMs': 15018 if failure else 6203, 'outcome': 'timeout' if failure else 'success', 'samples': []},
            'driver-provenance.json': {'variant': 'tauri-driver-2.1.0-stage-1', 'binarySha256': 'a' * 64},
            **({} if failure else {'webview-result.json': {'phase': 'webview-observed-awaiting-normal-close',
                'nativeTestsStarted': 1, 'checks': 3, 'actualRustIpc': True, 'browserVersion': '131.0.2903.86',
                'keyboard': {'cycles': 3, 'checks': 22, 'refreshes': 3}}})}


class DiagnosticTests(unittest.TestCase):
    def test_original_symptom_is_red_and_cleanup_does_not_substitute_for_close(self):
        row, reason = d.verdict(1, 0, 0, reports(True))
        self.assertEqual(reason, 'matching-session-timeout')
        self.assertTrue(row['matchingTimeout'])
        self.assertTrue(row['cleanupConfirmed'])
        self.assertFalse(row['normalCloseVerified'])

    def test_timeout_with_headers_is_not_a_matching_trace(self):
        data = reports(True)
        data['driver-stage-report.json']['observedStages'].append('native-headers')
        row, reason = d.verdict(1, 0, 0, data)
        self.assertFalse(row['matchingTimeout'])
        self.assertEqual(reason, 'unexpected-failure')

    def test_any_unknown_or_remaining_owned_resource_stops(self):
        for key, value in [('queryComplete', False), ('appRemaining', 1), ('driversRemaining', None),
                           ('profileProcessesRemaining', 1), ('harnessNodesRemaining', 1), ('listenersRemaining', 1)]:
            data = reports()
            data['cleanup-audit.json'][key] = value
            self.assertEqual(d.verdict(0, 0, 0, data)[1], 'cleanup-uncertain-or-failed')
        for key in ['cleanupFailures', 'childHandleDisposeFailures']:
            data = reports()
            data['native-result.json'][key] = 1
            self.assertEqual(d.verdict(0, 0, 0, data)[1], 'cleanup-uncertain-or-failed')
        data = reports()
        data['native-result.json']['afterDriverCleanupFacts']['rows'][0]['handleState'] = 'unavailable'
        self.assertEqual(d.verdict(0, 0, 0, data)[1], 'cleanup-uncertain-or-failed')

    def test_sanitizer_rejects_paths_payloads_unknown_fields_duplicates_and_oversize(self):
        for value in [{'path': 'PRIVATE_MARKER'}, {'status': 'C:\\private\\secret'},
                      {'facts': {'stdout': {'hints': {'authorization': 'PRIVATE_MARKER'}}}},
                      {'errorCode': 'NATIVE_TIMEOUT\nPRIVATE_MARKER'}, {'elapsedMs': float('nan')},
                      {'requests': [None] * 513}]:
            with self.assertRaises(ValueError):
                d.safe(value)
        for data in reports().values():
            self.assertEqual(d.safe(data), data)
        with tempfile.TemporaryDirectory() as folder:
            file = Path(folder) / 'x.json'
            for raw in ['{"status":"ok","status":"failed"}', 'x' * 262145]:
                file.write_text(raw)
                with self.assertRaises(ValueError):
                    d.read(file)

    def loop(self, sequence):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            source, control = root / 'source', root / 'control'
            d.write(source / 'ui/tauri/native-smoke/tools.json', {'driverExeSha256': 'a' * 64})
            build = {'hashes': {'exeSha256': 'a' * 64, 'archiveSha256': 'a' * 64},
                     'driverProvenance': {'binarySha256': 'a' * 64}, 'edgeDriverSha256': 'a' * 64}
            calls = []

            def command(args, **kwargs):
                if '--verify-patched' in args:
                    d.write(Path(args[args.index('--provenance') + 1]), build['driverProvenance'])
                    return 0
                script = next(a for a in args if a.endswith('.ps1'))
                if script.endswith('supervise.ps1'):
                    index = len(calls)
                    calls.append(args)
                    data, exit_code = sequence[min(index, len(sequence) - 1)]
                    output = Path(args[args.index('-OutputDirectory') + 1])
                    for name, value in data.items():
                        d.write(output / name, copy.deepcopy(value))
                    if isinstance(exit_code, Exception):
                        raise exit_code
                    return exit_code
                return 0

            with patch.object(d.sys, 'platform', 'win32'), patch.dict(d.os.environ, RUNNER_ENVIRONMENT='github-hosted'), \
                 patch.object(d, 'identity', return_value=build), patch.object(d, 'verify_source'), \
                 patch.object(d, 'digest', return_value='a' * 64), \
                 patch.object(d, 'copy_tools', side_effect=lambda prepared, trial: trial.mkdir()), \
                 patch.object(d, 'command', side_effect=command):
                code = d.run(source, control, root)
            if isinstance(sequence[0][1], Exception):
                self.assertTrue((root / 'safe/trial-01/native-observation.json').is_file())
                self.assertTrue((root / 'safe/trial-01/cleanup-audit.json').is_file())
            return code, d.read(root / 'safe/aggregate.json'), calls

    def test_serial_loop_stops_at_first_matching_timeout_without_replacement(self):
        code, result, calls = self.loop([(reports(), 0), (reports(True), 1), (reports(), 0)])
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 2)
        self.assertEqual(result['attempted'], 2)
        self.assertEqual(result['stopReason'], 'matching-session-timeout')
        self.assertTrue(result['pr200Blocked'])
        self.assertNotEqual(calls[0][calls[0].index('-ToolsDirectory') + 1],
                            calls[1][calls[1].index('-ToolsDirectory') + 1])

    def test_no_reproduction_cap_is_inconclusive_and_still_blocked(self):
        code, result, calls = self.loop([(reports(), 0)])
        self.assertEqual(code, 0)
        self.assertEqual(len(calls), 10)
        self.assertEqual(result['stopReason'], 'no-reproduction-cap-inconclusive')
        self.assertEqual(result['interpretation'], 'inconclusive')
        self.assertTrue(result['pr200Blocked'])

    def test_unexpected_failure_and_rejected_evidence_stop_without_retry(self):
        for data, expected in [(reports(), 'unexpected-failure'),
                               ({**reports(), 'session-startup-timing.json': {'path': 'PRIVATE_MARKER'}},
                                'unsafe-or-incomplete-evidence')]:
            code, result, calls = self.loop([(data, 1), (reports(), 0)])
            self.assertEqual(code, 1)
            self.assertEqual(len(calls), 1)
            self.assertEqual(result['stopReason'], expected)

    def test_wrapper_timeout_salvages_observations_and_audits_without_retry(self):
        code, result, calls = self.loop([(reports(True), TimeoutError('PRIVATE_RAW_EXCEPTION')),
                                         (reports(), 0)])
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 1)
        self.assertEqual(result['stopReason'], 'wrapper-failure-cleanup-unknown')
        self.assertTrue(result['trials'][0]['matchingTimeout'])
        self.assertNotIn('PRIVATE_RAW_EXCEPTION', json.dumps(result))

    def test_missing_mandatory_observation_stops_and_known_failure_facts_are_safe(self):
        for name in ['session-startup-timing.json', 'driver-provenance.json', 'webview-result.json']:
            data = reports()
            del data[name]
            code, result, calls = self.loop([(data, 0), (reports(), 0)])
            self.assertEqual(code, 1)
            self.assertEqual(len(calls), 1)
            self.assertEqual(result['stopReason'], 'unsafe-or-incomplete-evidence')
            self.assertIn(name, result['trials'][0]['missingEvidenceFiles'])
        data = reports(True)
        data['native-result.json']['beforeSessionObservation'] = 'unavailable'
        self.assertEqual(d.safe(data['native-result.json']), data['native-result.json'])
        self.assertEqual(d.safe({'code': 'TRACE_INVALID'}), {'code': 'TRACE_INVALID'})


if __name__ == '__main__':
    unittest.main()
