"""Pure schema/projection/cap tests; shallow checkouts contain no native processes."""
import copy
import importlib.util
import contextlib
import io
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import driver
import legacy
import overlay
import pins
import probe_schema
import publish
import runner

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('original_stage_validator', ROOT / 'scripts/verify_driver_trace.py')
sys.path.insert(0, str(ROOT / 'scripts'))
original = importlib.util.module_from_spec(spec)
spec.loader.exec_module(original)


def raw(assigned=True):
    data = json.loads((ROOT / 'diagnostics/session-forward/records/pr231-failure.json').read_text())['driver-stage-report.json']
    rows = [{'type': 'header', 'schema': 1, 'variant': probe_schema.VARIANT}, *data['events'], data['summary']]
    for row in rows[1:-1]:
        row['connectionAssigned'] = assigned if row['stage'] == 'request-dropped' else None
    return rows


def encode(rows):
    return '\n'.join(json.dumps(row, separators=(',', ':')) for row in rows).encode()


def state():
    return {'schema': 1, 'stages': {s: 'passed' for s in runner.STAGES}, 'nativeInvoked': True, 'auditInvoked': True}


def synthetic_success():
    # Synthetic decision fixture, not a new native observation.
    rows = raw(True)
    for row in rows[1:-1]:
        if row['stage'] == 'request-dropped':
            row.update(stage='native-headers', state='headers-returned')
        if row['stage'] == 'connection-end' and row['request']:
            row.update(state='headers-returned', error='none')
    stage, capture = probe_schema.inspect(encode(rows), original.inspect)
    files = {n: {} for n in publish.FILE_NAMES - {'outcome.json'}}
    files['driver-stage-report.json'] = stage
    files['native-connection-record.json'] = capture
    files['native-result.json'] = {'nativeTestsRun': 1, 'nativeTestsPassed': 1,
        'normalCloseVerified': True, 'cleanupStarted': True, 'cleanupFailures': 0,
        'childHandleDisposeFailures': 0, 'driverExitObservedAfterCleanup': True,
        'afterDriverCleanupFacts': {'observed': 1, 'remainingSnapshot': 0, 'omittedRows': 0,
            'rows': [{'handleIdentityVerified': True, 'handleState': 'exited', 'snapshotState': 'absent'}]}}
    files['cleanup-audit.json'] = {'queryComplete': True, **{k: 0 for k in
        ('appRemaining', 'driversRemaining', 'profileProcessesRemaining', 'harnessNodesRemaining', 'listenersRemaining')}}
    files['native-observation.json'] = {'status': 'webview-passed-awaiting-close', 'droppedRequests': 0,
        'requests': [{'operation': 'create-session', 'outcome': 'ok', 'elapsedMs': 1, 'status': 200}]}
    files['trial-exits.json'] = {'schema': 1, **{k: True for k in
        ('supervisorSucceeded', 'inspectorSucceeded', 'auditSucceeded', 'identityStable')}}
    return files


class SchemaTests(unittest.TestCase):
    def test_true_false_and_null_keep_original_stage_projection(self):
        for assigned in (True, False, None):
            stage, capture = probe_schema.inspect(encode(raw(assigned)), original.inspect)
            self.assertEqual(stage['requestStage'], 'dropped-before-headers')
            self.assertIs(capture['connectionAssigned'], assigned)
            self.assertEqual(capture['captureComplete'], assigned is not None)
            probe_schema.validate_capture(capture)

    def test_missing_extra_nonboolean_and_wrong_variant_rejected(self):
        cases = []
        for change in ('missing', 'extra', 'integer', 'nonterminal', 'variant', 'request'):
            rows = raw()
            if change == 'missing': del rows[6]['connectionAssigned']
            if change == 'extra': rows[6]['PRIVATE_KEY'] = 'PRIVATE_VALUE'
            if change == 'integer': rows[6]['connectionAssigned'] = 1
            if change == 'nonterminal': rows[2]['connectionAssigned'] = True
            if change == 'variant': rows[0]['variant'] = 'tauri-driver-2.1.0-stage-1'
            if change == 'request': rows[6]['request'] = 2
            cases.append(encode(rows))
        for data in cases:
            with self.assertRaises((ValueError, KeyError)):
                probe_schema.inspect(data, original.inspect)

    def test_duplicate_truncated_oversize_and_boolean_integer_rejected(self):
        good = encode(raw())
        cases = [good.replace(b'"schema":1', b'"schema":1,"schema":1', 1),
                 good[:-10], b'x' * 8193, good.replace(b'"seq":1', b'"seq":true', 1)]
        for data in cases:
            with self.assertRaises((ValueError, KeyError)):
                probe_schema.inspect(data, original.inspect)

    def test_overflow_and_null_are_unavailable_not_pass(self):
        rows = raw(True)
        rows[-1]['dropped'] = 1
        _, capture = probe_schema.inspect(encode(rows), original.inspect)
        self.assertEqual(capture, probe_schema.unavailable())
        probe_schema.validate_capture(capture)
        capture['captureComplete'] = True
        with self.assertRaises(ValueError): probe_schema.validate_capture(capture)

    def test_header_with_false_metadata_is_inconsistent(self):
        rows = raw(False)
        for row in rows[1:-1]:
            if row['stage'] == 'request-dropped': row.update(stage='native-headers', state='headers-returned')
            if row['stage'] == 'connection-end' and row['request']: row.update(state='headers-returned', error='none')
        with self.assertRaises(ValueError): probe_schema.inspect(encode(rows), original.inspect)

    def test_unknown_capture_fields_or_classification_never_accepted(self):
        _, capture = probe_schema.inspect(encode(raw()), original.inspect)
        for key, value in [('classification', 'PRIVATE_VALUE'), ('connection', True), ('schema', True), ('PRIVATE_KEY', 'PRIVATE_VALUE')]:
            bad = dict(capture); bad[key] = value
            with self.assertRaises(ValueError): probe_schema.validate_capture(bad)

    def test_unknown_legacy_keys_words_and_nonfinite_numbers_rejected(self):
        for value in ({'PRIVATE_KEY': True}, {'errorCode': 'NATIVE_PRIVATE_SECRET'}, {'elapsedMs': float('nan')}, {'phase': '/PRIVATE/PATH'}):
            with self.assertRaises(ValueError): legacy.safe(value)
        for name, value in [('native-observation.json', {'requests': [1], 'droppedRequests': 0}),
                            ('native-result.json', []), ('driver-stage-report.json', [])]:
            with self.assertRaises(ValueError): publish.native_file(name, value)

    def test_complete_record_hashes_and_size_guard(self):
        files = {'native-connection-record.json': probe_schema.unavailable()}
        record = json.loads(publish.encode(files))
        self.assertEqual(record['fileSha256']['native-connection-record.json'], overlay.sha(json.dumps(files['native-connection-record.json'], sort_keys=True, separators=(',', ':')).encode()))
        with self.assertRaises(ValueError): publish.encode({'PRIVATE_FILENAME': {}})
        with self.assertRaises(ValueError): publish.encode({'native-observation.json': {'schema': 1, 'requests': ['x' * 65536]}})


class DecisionTests(unittest.TestCase):
    def test_success_is_only_inconclusive_and_missing_any_file_cannot_pass(self):
        files = synthetic_success()
        result, passed = publish.decision(files, state(), True, publish.missing(files, state()))
        self.assertTrue(passed)
        self.assertEqual(result['interpretation'], 'no-repro-inconclusive')
        self.assertFalse(result['rootCauseEstablished'] or result['timeoutFixed'])
        for name in files:
            reduced = {k: v for k, v in files.items() if k != name}
            _, passed = publish.decision(reduced, state(), True, publish.missing(reduced, state()))
            self.assertFalse(passed, name)

    def test_cleanup_identity_null_and_inspector_failure_stop(self):
        for change in ('cleanup', 'identity', 'capture', 'inspector'):
            files = synthetic_success()
            if change == 'cleanup': files['cleanup-audit.json']['appRemaining'] = 1
            if change == 'capture': files['native-connection-record.json'] = probe_schema.unavailable()
            if change == 'inspector': files['trial-exits.json']['inspectorSucceeded'] = False
            result, passed = publish.decision(files, state(), change != 'identity', [])
            self.assertFalse(passed)
            self.assertTrue(result['nativeSucceeded'])
        files = synthetic_success(); del files['native-result.json']
        result, passed = publish.decision(files, state(), True, ['native-result.json'])
        self.assertFalse(passed); self.assertTrue(result['supervisorSucceeded']); self.assertIsNone(result['nativeSucceeded'])

    def test_matching_timeout_and_http_body_timeout_stay_distinct(self):
        files = synthetic_success()
        saved = json.loads((ROOT / 'diagnostics/session-forward/records/pr231-failure.json').read_text())
        stage, capture = probe_schema.inspect(encode(raw()), original.inspect)
        files.update(saved); files['driver-stage-report.json'] = stage; files['native-connection-record.json'] = capture
        files['native-result.json']['nativeTestsPassed'] = 0
        result, passed = publish.decision(files, state(), True, [])
        self.assertTrue(result['matchingTimeout']); self.assertFalse(passed)
        files['native-observation.json']['requests'][-1]['status'] = 200
        result, _ = publish.decision(files, state(), True, [])
        self.assertFalse(result['matchingTimeout'])

    def test_stage_order_and_reentry_or_audit_without_native_rejected(self):
        bad = {'schema': 1, 'stages': {s: 'not-run' for s in runner.STAGES}, 'nativeInvoked': False, 'auditInvoked': True}
        with self.assertRaises(ValueError): runner.validate_state(bad)
        bad['auditInvoked'] = False; bad['stages']['build'] = 'passed'
        with self.assertRaises(ValueError): runner.validate_state(bad)
        with patch.object(runner, 'command', side_effect=AssertionError('MUST_NOT_LAUNCH')):
            with self.assertRaises(ValueError): runner.trial(ROOT, Path('/not-used'), state())

    def test_partial_transport_failure_records_preserve_python_pass(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder); (root / 'safe').mkdir(); (root / 'private').mkdir()
            fake = type('Prepare', (), {'prepare': staticmethod(lambda *args: None)})
            with patch.object(driver, 'module', return_value=fake), patch.object(driver, 'quiet', side_effect=[None, subprocess.TimeoutExpired('PRIVATE', 1)]):
                with self.assertRaises(subprocess.TimeoutExpired): driver.preflight(root, root / 'PRIVATE.crate')
            value = json.loads((root / 'safe/transport-preflight.json').read_text())
            self.assertEqual(value['fixedPython'], 'passed'); self.assertEqual(value['realHandler'], 'timeout')
            self.assertEqual(value['fixedPythonPassed'], 5); self.assertEqual(value['nativeTrials'], 0)
            publish.transport(value)
            with self.assertRaises(ValueError): driver.preflight(root, root / 'PRIVATE.crate')

    def test_verified_driver_binary_binding_cannot_be_replaced_after_build(self):
        # Synthetic metadata transition, no executable is run.
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder); (root / 'safe').mkdir(); (root / 'tools').mkdir()
            archive = root / 'tools/tauri-driver.crate'; archive.write_bytes(b'SYNTHETIC_ARCHIVE')
            binary = root / 'tools/tauri/bin/tauri-driver.exe'; binary.parent.mkdir(parents=True); binary.write_bytes(b'SYNTHETIC_BINARY_ONE')
            source = root / 'tools/tauri-driver-2.1.0'; source.mkdir()
            scope = root / 'diagnostic-scope/diagnostics/session-forward'; scope.mkdir(parents=True); (scope / 'connection.patch').write_bytes(b'SYNTHETIC_PATCH')
            stage2 = {name: b'SYNTHETIC_SOURCE' for name in ('src/main.rs', 'src/server.rs', 'src/diagnostic.rs', 'src/diagnostic_tests.rs')}
            base = type('Base', (), {'check_tree': staticmethod(lambda *args: None)})
            before = driver.provenance(root, archive, stage2, b'SYNTHETIC_BASE')
            (root / 'tools/driver-provenance.json').write_text(json.dumps(before))
            with patch.object(driver, 'images', return_value=(base, {}, {}, stage2, b'SYNTHETIC_BASE')):
                driver.perform('verify', root, source, archive, binary)
                saved = (root / 'safe/driver-provenance.json').read_bytes()
                binary.write_bytes(b'SYNTHETIC_BINARY_TWO')
                with self.assertRaises(ValueError): driver.perform('verify', root, source, archive, binary)
            self.assertEqual((root / 'safe/driver-provenance.json').read_bytes(), saved)

    def test_cache_and_record_type_mutations_rejected(self):
        good = {'schema': 1, 'keySha256Suffix': 'a' * 64, 'lookupExact': True, 'exactHit': True, 'restoreOnly': True, 'state': 'exact-hit'}
        publish.cache(good)
        for key, value in [('schema', True), ('keySha256Suffix', 'PRIVATE'), ('restoreOnly', False), ('state', 'PRIVATE')]:
            bad = dict(good); bad[key] = value
            with self.assertRaises(ValueError): publish.cache(bad)
        with self.assertRaises(ValueError): publish.transport({'schema': 1})

    def test_cli_errors_never_echo_arbitrary_arguments(self):
        for name, expected in [('runner.py', 'FORWARD_STAGE_REJECTED\n'), ('driver.py', 'FORWARD_DRIVER_STAGE_REJECTED\n')]:
            result = subprocess.run([sys.executable, '-B', str(Path(__file__).parent / name), '--PRIVATE_OPTION', 'PRIVATE_VALUE'], capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 1); self.assertEqual(result.stdout, expected); self.assertEqual(result.stderr, '')

    def test_command_ledger_preserves_exit_then_contract_failure_and_blocks_reentry(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder); (root / 'private').mkdir(); (root / 'safe').mkdir()
            with patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 0)):
                with self.assertRaises(ValueError): runner.checked(root, 'control-python', ['PRIVATE'], expected='MISSING_EXPECTED_COUNT')
                with self.assertRaises(ValueError): runner.command(root, 'control-python', ['PRIVATE'])
            value = runner.validate_ledger(json.loads((root / 'safe/command-ledger.json').read_text()))
            self.assertEqual(value['commands']['control-python'], {'state': 'succeeded', 'exitCode': 0, 'outputContractVerified': False})
            with patch.object(runner, 'versions'), patch.object(runner, 'checked') as checked:
                runner.perform('preflight', ROOT, root, {})
            call = next(call for call in checked.call_args_list if call.args[1] == 'original-node')
            self.assertIn('--test-reporter=tap', call.args[2])
            good = '# tests 15\n# pass 15\n# fail 0\n# cancelled 0\n# skipped 0\n# todo 0'
            self.assertIsNotNone(re.search(call.kwargs['expected'], good))
            self.assertIsNone(re.search(call.kwargs['expected'], good.replace('# skipped 0', '# skipped 1')))


class CheckoutTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.base = Path(cls.temp.name)
        cls.source = cls.base / 'source'
        origins = [(os.environ.get('FORWARD_TEST_PRODUCT', str(ROOT)), pins.PRODUCT, cls.source),
                   (os.environ.get('FORWARD_TEST_DIAGNOSTIC', str(ROOT)), pins.DIAGNOSTIC, cls.source / '.diagnostic')]
        for origin, sha, target in origins:
            cls.clone(Path(origin), sha, target)
        control = Path(os.environ.get('FORWARD_TEST_CONTROL', str(ROOT)))
        cls.control_sha = overlay.git(control, 'rev-parse', 'HEAD')
        cls.clone(control, cls.control_sha, cls.source / '.control')

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    @staticmethod
    def clone(origin, sha, target, crlf=False):
        target.mkdir(parents=True)
        for args in (['init'], ['config', 'core.autocrlf', 'true' if crlf else 'false'],
                     ['fetch', '--depth', '1', origin.resolve().as_uri(), sha], ['checkout', '--detach', 'FETCH_HEAD']):
            subprocess.run(['git', '-C', str(target), *args], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)

    def test_independent_shallow_lf_and_crlf_whole_overlay(self):
        for newline in ('LF', 'CRLF'):
            source = self.source
            if newline == 'CRLF':
                source = self.base / 'source-CRLF'
                self.clone(Path(os.environ.get('FORWARD_TEST_PRODUCT', str(ROOT))), pins.PRODUCT, source, True)
                self.clone(Path(os.environ.get('FORWARD_TEST_DIAGNOSTIC', str(ROOT))), pins.DIAGNOSTIC, source / '.diagnostic', True)
                self.assertIn(b'\r\n', (source / 'ui/tauri/native-smoke/supervise.ps1').read_bytes())
            diagnostic = source / '.diagnostic'
            target = self.base / ('output-' + newline)
            digest, count = overlay.materialize(source, diagnostic, target, True)
            self.assertEqual(len(digest), 64); self.assertGreater(count, 10)
            overlay.materialize(source, diagnostic, target)
            p = target / 'overlay/ui/tauri/native-smoke/supervise.ps1'
            p.write_bytes(p.read_bytes() + b'PRIVATE_MUTATION')
            with self.assertRaises(ValueError): overlay.materialize(source, diagnostic, target)

    def test_wrong_missing_swapped_nested_source_pins_fail(self):
        for root, head, tree in [(self.source, pins.DIAGNOSTIC, pins.DIAGNOSTIC_TREE),
            (self.source / '.diagnostic', pins.PRODUCT, pins.PRODUCT_TREE),
            (self.base / 'missing', pins.PRODUCT, pins.PRODUCT_TREE),
            (self.source / 'ui', pins.PRODUCT, pins.PRODUCT_TREE)]:
            with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                overlay.checkout(root, head, tree)

    def test_mixed_bare_cr_mutation_and_missing_projection_anchor_fail(self):
        name = 'ui/tauri/native-smoke/supervise.ps1'
        path = self.source / name; canonical = path.read_bytes()
        for bad in (canonical.replace(b'\n', b'\r\n', 1), canonical.replace(b'\n', b'\r', 1), canonical + b'PRIVATE_MUTATION'):
            try:
                path.write_bytes(bad)
                with self.assertRaises(ValueError): overlay.blob(self.source, name)
            finally: path.write_bytes(canonical)
        with self.assertRaises(ValueError): overlay.setup_projection(b'PRIVATE_NOT_SOURCE')
        with self.assertRaises(ValueError): overlay.supervisor_projection(canonical + canonical)

    def test_exact_supervisor_inverse_and_setup_count_guards(self):
        data = overlay.blob(self.source, 'ui/tauri/native-smoke/supervise.ps1')[1]
        changed = overlay.supervisor_projection(data)
        self.assertEqual(changed.replace(b'tauri-driver-2.1.0-connection-probe-1', b'tauri-driver-2.1.0-stage-1'), data)
        data = overlay.blob(self.source, 'ui/tauri/native-smoke/setup.ps1')[1]
        changed = overlay.setup_projection(data)
        self.assertEqual(changed.count(b'driver.py'), 2)
        self.assertIn(b'--locked', changed)
        with self.assertRaises(ValueError): overlay.setup_projection(data + data)

    def test_control_is_shallow_without_historical_source_objects(self):
        control = self.source / '.control'
        self.assertTrue((control / '.git/shallow').is_file())
        for sha in (pins.PRODUCT, pins.DIAGNOSTIC):
            result = subprocess.run(['git', '-C', str(control), 'cat-file', '-e', sha + '^{commit}'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            self.assertNotEqual(result.returncode, 0)

    def test_full_boundary_and_event_attempt_draft_guards(self):
        event = {'action': 'opened', 'pull_request': {'draft': True,
            'head': {'sha': self.control_sha, 'ref': pins.BRANCH, 'repo': {'full_name': 'O2-CONNECTIVE/rescue-host'}},
            'base': {'ref': pins.BASE, 'sha': pins.BASE_SHA}}}
        event_path = self.base / 'event.json'
        event_path.write_text(json.dumps(event))
        env = {'GITHUB_EVENT_PATH': str(event_path), 'GITHUB_RUN_ATTEMPT': '1',
            'GITHUB_REPOSITORY': 'O2-CONNECTIVE/rescue-host', 'EXPECTED_CONTROL': self.control_sha,
            'GITHUB_WORKSPACE': str(self.source), 'RUNNER_TEMP': str(self.base),
            'RUNNER_ENVIRONMENT': 'github-hosted', 'RUNNER_OS': 'Windows', 'RUNNER_ARCH': 'X64'}
        root = self.base / 'pr231-connection-once'
        with patch.dict(os.environ, env):
            runner.boundary(self.source, root, True)  # Synthetic platform guard, no process launch.
            with self.assertRaises(ValueError): runner.boundary(self.source, self.base / 'different-root')
            for mutate in ('draft', 'branch', 'base', 'attempt', 'action'):
                bad = copy.deepcopy(event)
                if mutate == 'draft': bad['pull_request']['draft'] = False
                if mutate == 'branch': bad['pull_request']['head']['ref'] = 'PRIVATE_BRANCH'
                if mutate == 'base': bad['pull_request']['base']['sha'] = '0' * 40
                if mutate == 'action': bad['action'] = 'synchronize'
                event_path.write_text(json.dumps(bad))
                with patch.dict(os.environ, {'GITHUB_RUN_ATTEMPT': '2' if mutate == 'attempt' else '1'}):
                    with self.assertRaises(ValueError): runner.boundary(self.source, root)
            event_path.write_text(json.dumps(event))
            runner.identity(self.source, root)
            failed = {'schema': 1, 'stages': {s: 'failed' if s == 'environment' else 'not-run' for s in runner.STAGES}, 'nativeInvoked': False, 'auditInvoked': False}
            runner.write(root / 'safe/run-status.json', failed)
            runner.write(root / 'observation/native-observation.json', [1])
            file = self.source / 'ui/tauri/native-smoke/supervise.ps1'
            original_bytes = file.read_bytes()
            try:
                file.write_bytes(original_bytes + b'PRIVATE_SOURCE_DRIFT')
                output = io.StringIO()
                with patch.dict(os.environ, {'GITHUB_STEP_SUMMARY': str(self.base / 'summary')}), contextlib.redirect_stdout(output):
                    self.assertEqual(publish.finalize(self.source, root), 1)
                record = json.loads((root / 'safe/canonical-record.json').read_text())
                self.assertIn('build-identity.json', record['files'])
                result = record['files']['outcome.json']
                self.assertFalse(result['sourceAndSpecimenStable'])
                self.assertEqual(result['nativeTestsObserved'], 0)
                self.assertIn('native-observation.json', result['rejectedFiles'])
                self.assertNotIn('PRIVATE', output.getvalue())
                with patch.dict(os.environ, {'GITHUB_STEP_SUMMARY': str(self.base / 'summary')}), contextlib.redirect_stdout(io.StringIO()), patch.object(publish, 'decision', side_effect=ValueError('PRIVATE_EXCEPTION')):
                    self.assertEqual(publish.finalize(self.source, root), 1)
                record = json.loads((root / 'safe/canonical-record.json').read_text())
                self.assertIn('build-identity.json', record['files'])
                self.assertEqual(record['files']['outcome.json']['interpretation'], 'evidence-evaluation-uncertain')
                self.assertNotIn('PRIVATE', json.dumps(record))
            finally:
                file.write_bytes(original_bytes)


if __name__ == '__main__':
    unittest.main()
