"""Closed control qualification and publication, without native launches."""
import contextlib
import io
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import post_verdict_anchor as control

BIND = {'controlCommit': 'c'*40, 'sourceCommit': control.SOURCE, 'sourceTree': control.TREE}

class AnchorControlTests(unittest.TestCase):
    def test_executed_ids_not_inherited_counts(self):
        class Good(unittest.TestCase):
            def runTest(self): pass
        suite = unittest.TestSuite([Good()])
        ids = [next(control.cases(suite)).id()]
        with patch.object(control, 'suite', return_value=suite), patch.object(control, 'TEST_IDS', ids), contextlib.redirect_stderr(io.StringIO()):
            value = control.qualify(Path('.'), BIND)
        self.assertEqual(value['started'], 1)
        self.assertEqual(value['testIdsSha256'], control.digest(ids))
        self.assertEqual(value['nativeLaunches'], 0)

    def test_skip_zero_and_wrong_ids_rejected(self):
        class Skip(unittest.TestCase):
            def runTest(self): self.skipTest('fixture')
        for suite, ids in ((unittest.TestSuite(), []), (unittest.TestSuite([Skip()]), ['different'])):
            with patch.object(control, 'suite', return_value=suite), patch.object(control, 'TEST_IDS', ids), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(ValueError): control.qualify(Path('.'), BIND)

    def test_original_failed_exit_survives_causal_publication(self):
        from types import SimpleNamespace
        body = {**BIND, 'files': {name: {'data': {}, 'sha256': control.digest({})} for name in control.SECTIONS}}
        text = ('WRY_DIAGNOSTIC_RECORD_BEGIN\n' + control.canonical(body).decode()
                + '\nWRY_DIAGNOSTIC_RECORD_SHA256=' + control.digest(body) + '\nWRY_DIAGNOSTIC_RECORD_END\n')
        def original(*args, **kwargs):
            print(text, end=''); return False
        for original_passed in (False, True):
            def original(*args, **kwargs):
                print(text, end=''); return original_passed
            for claim in ('unknown', 'controller-pre-send-observation-after-verdict'):
                output = io.StringIO()
                with patch('post_verdict_publish.companion', return_value={'claim': claim}), contextlib.redirect_stdout(output):
                    result = control.publish(Path('.'), Path('.'), SimpleNamespace(publish=original), BIND)
                self.assertEqual(result, 0 if original_passed else 1)
                self.assertTrue(output.getvalue().startswith(text))
                self.assertIn('"claim":"' + claim + '"', output.getvalue())
        original_passed = False
        for error in (ValueError('diagnostic rejected'), OSError('diagnostic unavailable'), RecursionError('nested private path')):
            with patch.object(control, 'binding', return_value=BIND), \
                 patch.object(control, 'source_module', return_value=SimpleNamespace(publish=original)), \
                 patch('post_verdict_publish.companion', side_effect=error), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(control.main(['publish', '--root', '.', '--temp', '.']), 1)

    def test_closed_manifest_no_public_nonce(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            value = control.manifest(BIND, 'a'*64, '12345')
            self.assertEqual(value['attempt'], 1)
            self.assertEqual(value['trial'], 1)
            self.assertEqual(len(value['nonce']), 64)
            control.write_new(target / 'manifest.json', value)
            with self.assertRaises(FileExistsError): control.write_new(target / 'manifest.json', value)
            for run in ('', '01', 'abc', '-1'):
                with self.assertRaises(ValueError): control.manifest(BIND, 'a'*64, run)

    def test_publication_receipt_after_successful_output_only(self):
        value = control.expected(BIND)
        source = type('Source', (), {'load': staticmethod(lambda *args: value)})
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory)
            with patch.object(control, 'emit', side_effect=OSError('closed')):
                with self.assertRaises(OSError): control.publish_qualification(temp, source, BIND)
            self.assertFalse((temp / 'post-verdict-qualification-published.json').exists())

class PublicationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        import os
        import post_verdict_publish as publisher
        import json
        import subprocess
        cls.root = Path(os.environ.get('POST_VERDICT_TEST_SOURCE') or os.environ['WRY_SOURCE']).resolve()
        cls.failure = publisher.module(cls.root, 'post_verdict_failure')
        code = ("import sys,json;sys.path.insert(0,'scripts/tests');"
                "import test_wry_post_verdict as f;t=f.AnchorTests();t.setUpClass();"
                "\ntry: print(json.dumps(t.observe('positive')))\nfinally: t.tearDownClass()")
        # A fresh interpreter keeps retired control overlay modules out of the
        # separately pinned source's real Rust fixture. No GUI/native is launched.
        result = subprocess.run([sys.executable, '-B', '-c', code], cwd=cls.root,
                                capture_output=True, timeout=30)
        if result.returncode: raise AssertionError(result.stderr.decode())
        cls.positive = json.loads(result.stdout)

    def setUp(self):
        import os
        context = patch.dict(os.environ, {'GITHUB_RUN_ID': '12345', 'GITHUB_RUN_ATTEMPT': '1'})
        context.start(); self.addCleanup(context.stop)

    def test_wrong_run_attempt_or_admission_manifest_rejected(self):
        import os
        import post_verdict_publish as publisher
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory); bind, original = self.prepare(temp)
            for key, wrong in (('GITHUB_RUN_ID', '98765'), ('GITHUB_RUN_ATTEMPT', '2')):
                with patch.dict(os.environ, {key: wrong}):
                    with self.assertRaises(ValueError): publisher.verify(self.root, temp, bind, original)
            marker = temp / 'post-verdict-admitted.json'
            value = self.failure.decode(marker.read_bytes()); value['manifestSha256'] = '0'*64
            marker.write_bytes(self.failure.canonical(value))
            with self.assertRaises(ValueError): publisher.verify(self.root, temp, bind, original)

    def prepare(self, temp):
        import copy
        import hashlib
        import json
        trace = temp / 'tools/app-startup'; trace.mkdir(parents=True)
        evidence = temp / 'evidence'; evidence.mkdir()
        f = self.failure
        bind = {'controlCommit': 'c'*40, 'sourceCommit': 'd'*40, 'sourceTree': 'e'*40}
        manifest = control.manifest(bind, 'f'*64, '12345')
        for path in (temp, trace): (path / 'post-verdict.manifest.json').write_bytes(f.canonical(manifest))
        import post_verdict_engine as engine
        from test_post_verdict_engine import fixture
        qualified = {**fixture(), **bind}
        (temp / 'post-verdict-engine-qualified.json').write_bytes(engine.canonical(qualified))
        with contextlib.redirect_stdout(io.StringIO()): engine.publish(temp, bind)
        admitted = {'qualificationSha256': control.digest(control.expected(bind)),
                    'engineQualificationSha256': engine.digest(qualified),
                    'manifestSha256': control.digest(manifest), **bind}
        (temp / 'post-verdict-admitted.json').write_bytes(f.canonical(admitted))
        baseline = json.loads((self.root / 'scripts/tests/fixtures/wry240-baseline.json').read_text())
        timing = {'schema': 1, 'purpose': 'diagnostic-hints-only', 'settledMs': 15009,
                  'outcome': 'timeout', 'samples': [{'stage': 'after-settlement', 'startedMs': 15009,
                  'completedMs': 15010, 'requestPendingAtStart': False, 'facts': None}]}
        for saved, name, data in (('post-verdict-native.json', 'native-observation.json', baseline['observation']),
                                  ('post-verdict-timing.json', 'session-startup-timing.json', timing)):
            (trace / saved).write_bytes(f.canonical(data)); (evidence / name).write_bytes(f.canonical(data))
        receipt = f.receipt(trace); raw = f.canonical(receipt)
        (trace / 'post-verdict-failure.json').write_bytes(raw)
        signal = {'protocol': 'post-verdict-signal-1', 'manifestSha256': receipt['manifestSha256'],
                  'failureSha256': hashlib.sha256(raw).hexdigest(), **bind, 'specimenSha256': 'f'*64}
        (trace / 'post-verdict.signal').write_bytes(f.canonical(signal))
        value = copy.deepcopy(self.positive)
        value['anchor'].update(manifestSha256=signal['manifestSha256'], failureSha256=signal['failureSha256'])
        (trace / 'wry-post-verdict-probe.json').write_text(json.dumps(value, separators=(',', ':')))
        (evidence / 'native-result.json').write_bytes(f.canonical(baseline['native']))
        data = {'native': baseline['native'], 'delivery': value['delivery'],
                'driver': {'requestStage': 'dropped-before-headers'}, 'launch': {'exeSha256': 'f'*64},
                'final': {'exeSha256': 'f'*64}}
        original = {'files': {k: {'data': v, 'sha256': control.digest(v)} for k, v in data.items()}}
        return bind, original

    def test_real_copy_signal_record_path_positive_and_tamper_rejects(self):
        import post_verdict_publish as publisher
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory); bind, original = self.prepare(temp)
            result = publisher.verify(self.root, temp, bind, original)
            self.assertEqual(result['claim'], 'controller-pre-send-observation-after-verdict')
            for name in ('post-verdict-native.json', 'post-verdict-timing.json', 'post-verdict-failure.json',
                         'post-verdict.signal', 'post-verdict.manifest.json'):
                path = temp / 'tools/app-startup' / name; raw = path.read_bytes(); path.write_bytes(raw + b' ')
                with self.assertRaises(ValueError): publisher.verify(self.root, temp, bind, original)
                path.write_bytes(raw)
            original['files']['driver']['data']['requestStage'] = 'native-headers'
            with self.assertRaises(ValueError): publisher.verify(self.root, temp, bind, original)

    def test_original_failure_cannot_be_replaced_by_late_success(self):
        import post_verdict_publish as publisher
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory); bind, original = self.prepare(temp)
            original['files']['native']['data']['nativeTestsPassed'] = 1
            value = publisher.companion(self.root, temp, bind, original, 'a'*64)
            self.assertEqual(value['evidence'], {'state': 'unavailable-or-rejected', 'claim': 'unknown'})
            self.assertNotIn('nonce', str(value))

class WorkflowTests(unittest.TestCase):
    def test_cap_one_publication_order_and_required_engine_gate(self):
        root = Path(__file__).resolve().parents[2]
        text = (root / '.github/workflows/two-launch-repro.yml').read_text()
        for exact in ("github.run_attempt == 1", "github.event.number == 251", "github.event.pull_request.number == 251",
                      "github.event.before == 'ddf2bce36dc81fd74b463d9dd5ad5a33d11fc9b7'",
                      "github.event.after != ''", "github.event.after == github.event.pull_request.head.sha",
                      "github.event.repository.private == false", "github.event.pull_request.draft == true",
                      "github.event.pull_request.head.repo.full_name == github.repository",
                      "github.event.pull_request.base.sha == '3bd7ee59dedd38728b731994a4088bd7e8c0a39d'",
                      'runs-on: windows-2022', 'timeout-minutes: 35'):
            self.assertIn(exact, text)
        self.assertEqual(text.count('-StartupDiagnostic -StartupManifest'), 1)
        self.assertIn('-PostVerdictManifest "$env:WRY_TEMP/post-verdict.manifest.json"', text)
        for forbidden in ('workflow_dispatch', 'upload-artifact', 'strategy:', 'secrets.', 'actions/cache', 'prior_red_recovery.py'):
            self.assertNotIn(forbidden, text)
        steps = text.split('      - name: ')
        qualifier = next(s for s in steps if 'post_verdict_anchor.py" qualify ' in s)
        public = next(s for s in steps if 'post_verdict_anchor.py" qualification ' in s)
        native = next(s for s in steps if 'post_verdict_anchor.py" admit ' in s)
        engine = next(s for s in steps if 'post_verdict_anchor.py" engine-qualification ' in s)
        harness = next(s for s in steps if '-QualificationPath "$env:WRY_TEMP/post-verdict-engine-qualified.json"' in s)
        self.assertIn('Invoke-ProbeStep python {', qualifier)
        self.assertNotIn('Invoke-ProbeStep', public)
        self.assertNotIn('if:', public)
        self.assertLess(steps.index(qualifier), steps.index(public))
        self.assertNotIn('Invoke-ProbeStep', engine)
        self.assertNotIn('if:', engine)
        self.assertLess(steps.index(public), steps.index(harness))
        self.assertLess(steps.index(harness), steps.index(engine))
        self.assertLess(steps.index(engine), steps.index(native))
        self.assertIn('post-verdict-contract.test.ps1', text)

    def test_exact_source_pins_and_separate_checkout(self):
        text = (Path(__file__).resolve().parents[2] / '.github/workflows/two-launch-repro.yml').read_text()
        self.assertEqual(text.count('      WRY_PIN: ' + control.SOURCE), 1)
        self.assertEqual(text.count('      WRY_TREE: ' + control.TREE), 1)
        self.assertEqual(text.count('          ref: ' + control.SOURCE), 1)
        self.assertIn('path: .control', text)
        self.assertIn('path: .source', text)
