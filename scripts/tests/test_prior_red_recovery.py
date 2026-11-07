"""Control boundaries only; no Windows native process is started by these fixtures."""
import contextlib
import copy
import io
import os
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import prior_red_recovery as control

BIND = {'controlCommit': 'c'*40, 'sourceCommit': control.SOURCE, 'sourceTree': control.TREE}
ROOT = Path(__file__).resolve().parents[2]


class RecoveryTests(unittest.TestCase):
    def test_original_orchestration_and_one_native_call(self):
        original = (ROOT / '.github/workflows/wry-receiver-once.yml').read_text()
        current = (ROOT / '.github/workflows/two-launch-repro.yml').read_text()
        start = '      - name: Run original Host classification fixtures once\n'
        end = '      - name: Publish bounded sanitized JSON and hashes only\n'
        # Original setup, invocation, reports and final identity remain byte-for-byte.
        expected = original.split(start)[1].split(end)[0]
        actual = current.split(start)[1].split(end)[0]
        admission = ('            python -B "$env:GITHUB_WORKSPACE/.control/scripts/prior_red_recovery.py" admit --root $env:WRY_SOURCE --temp $env:WRY_TEMP\n'
                     "            if ($LASTEXITCODE -ne 0) { throw 'ADMISSION' }\n")
        self.assertIn(admission, actual)
        self.assertEqual(actual.replace(admission, ''), expected)
        self.assertEqual(current.count('-StartupDiagnostic -StartupManifest'), 1)
        self.assertNotIn('two_launch_repro.py', current)
        self.assertNotIn('WRY_PUMP_ENABLED:', current)
        self.assertIn('before == \'8e5d95c1a09cd84523c6dc2bc4b9f7481d7643f3\'', current)
        self.assertIn('prior_red_recovery.py" qualify', current)
        self.assertIn('prior_red_recovery.py" publish', current)
        for forbidden in ('workflow_dispatch', 'upload-artifact', 'strategy:', 'secrets.', 'actions/cache'):
            self.assertNotIn(forbidden, current)

    def test_wrong_source_tree_control_or_environment_rejected(self):
        env = {'CONTROL_HEAD': 'c'*40, 'WRY_PIN': control.SOURCE, 'WRY_TREE': control.TREE}
        for values in (['c'*40, control.SOURCE, control.TREE], ['d'*40],
                       ['c'*40, 'a'*40], ['c'*40, control.SOURCE, 'b'*40]):
            with patch.dict(os.environ, env), patch.object(control, 'git', side_effect=values):
                if len(values) == 3 and values[-1] == control.TREE:
                    self.assertEqual(control.binding(Path('source')), BIND)
                else:
                    with self.assertRaises(ValueError): control.binding(Path('source'))
        for name in env:
            with patch.dict(os.environ, {**env, name: ''}), patch.object(control, 'git', return_value='c'*40):
                with self.assertRaises(ValueError): control.binding(Path('source'))

    def test_closed_qualification(self):
        value = control.expected(BIND)
        candidates = [{}, {**value, 'passed': True}, {**value, 'skipped': 1},
                      {**value, 'sourceCommit': 'a'*40}, {**value, 'controlCommit': 'a'*40},
                      {**value, 'discovered': 41}, {**value, 'started': 30}, {**value, 'extra': 1}]
        source = SimpleNamespace(load=lambda *args: value)
        self.assertEqual(control.receipt(Path('.'), source, BIND), value)
        for candidate in candidates:
            source.load = lambda *args: candidate
            with self.assertRaises(ValueError): control.receipt(Path('.'), source, BIND)

    def test_qualifier_counts_actual_execution_and_rejects_skips_failures_zero(self):
        class Good(unittest.TestCase):
            def runTest(self): pass
        class Skip(unittest.TestCase):
            def runTest(self): self.skipTest('fixture')
        class Bad(unittest.TestCase):
            def runTest(self): self.fail('fixture')
        for case, count, passes in ((Good, 1, True), (Skip, 1, False), (Bad, 1, False), (Good, 0, False)):
            suite = unittest.TestSuite([case()] if count else [])
            with patch.object(control, 'TESTS', 1), patch.object(unittest.defaultTestLoader, 'discover', return_value=suite), contextlib.redirect_stderr(io.StringIO()):
                if passes: self.assertEqual(control.qualify(Path('.'), BIND), control.expected(BIND))
                else:
                    with self.assertRaises(ValueError): control.qualify(Path('.'), BIND)

    def record(self):
        return {**BIND, 'files': {name: {'data': {}, 'sha256': control.digest({})} for name in control.SECTIONS}}

    def text(self, body):
        return 'WRY_DIAGNOSTIC_RECORD_BEGIN\n' + control.canonical(body).decode() + '\nWRY_DIAGNOSTIC_RECORD_SHA256=' + control.digest(body) + '\nWRY_DIAGNOSTIC_RECORD_END\n'

    def test_exact_twelve_sections_and_partial_original_failure_preserved(self):
        body = self.record()
        text = self.text(body)
        self.assertEqual(control.validate_record(text, BIND), body)
        for passed in (True, False):
            def publish(*args, **kwargs):
                print(text, end='')
                return passed
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                result = control.publish(Path('.'), Path('.'), SimpleNamespace(publish=publish), BIND)
            self.assertEqual(result, 0 if passed else 1)
            self.assertEqual(output.getvalue(), text)
        for name in control.SECTIONS:
            bad = copy.deepcopy(body); del bad['files'][name]
            with self.assertRaises(ValueError): control.validate_record(self.text(bad), BIND)
        for change in ('extra', 'hash', 'source'):
            bad = copy.deepcopy(body)
            if change == 'extra': bad['files']['pump'] = {'data': {}, 'sha256': control.digest({})}
            if change == 'hash': bad['files']['native']['sha256'] = '0'*64
            if change == 'source': bad['sourceTree'] = '0'*40
            with self.assertRaises(ValueError): control.validate_record(self.text(bad), BIND)

    def test_admission_binds_specimen_harness_receipt_and_successful_setup(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); temp = root / 'temp'
            paths = ['unpacked/rescue-host-shell.exe', 'fixture/rescue-host-shell.exe', 'unpacked/BUILD.json',
                     'fixture/BUILD.json', 'tools/tauri/bin/tauri-driver.exe', 'fixture.zip',
                     'tools/edge/msedgedriver.exe', 'tools/tauri-driver-2.1.0/Cargo.lock']
            for name in paths:
                path = temp / name; path.parent.mkdir(parents=True, exist_ok=True); path.write_bytes(b'fixed')
            patch_file = root / 'tools/native-driver-trace/driver.patch'
            patch_file.parent.mkdir(parents=True); patch_file.write_bytes(b'fixed')
            identity = {'sourceCommit': control.SOURCE, 'sourceTree': control.TREE}
            launch = {**identity, **{key: 'hash' for key in ('exeSha256', 'driverSha256', 'archiveSha256',
                      'edgeDriverSha256', 'driverLockSha256', 'driverPatchSha256')}}
            stages = tuple(str(n) for n in range(16))
            steps = [{'stage': stage, 'exitCode': 0} for stage in stages[:12]]
            def load(path, *args):
                return {'prior-red-qualification.json': control.expected(BIND), 'steps.json': steps, 'launch.json': launch,
                        'BUILD.json': {'schema': 1, 'variant': 'host-startup-diagnostic-1', 'sourceCommit': control.SOURCE,
                        'features': ['custom-protocol', 'startup-diagnostics'], 'profile': 'release',
                        'exeSha256': 'hash', 'distribution': False}}[path.name]
            source = SimpleNamespace(load=load, safe_steps=lambda x: x, STEPS=stages,
                     permissions=lambda *args: None, source_identity=lambda *args: dict(identity),
                     digest=lambda x: 'hash' if x == b'fixed' else 'changed', safe_meta=lambda x, **kw: x)
            control.admit(root, temp, source, BIND)
            with self.assertRaises(FileExistsError): control.admit(root, temp, source, BIND)
            (temp / 'prior-red-admitted.json').unlink()
            for name in paths:
                path = temp / name; path.write_bytes(b'changed')
                with self.assertRaises(ValueError): control.admit(root, temp, source, BIND)
                path.write_bytes(b'fixed')
            steps[0]['exitCode'] = 1
            with self.assertRaises(ValueError): control.admit(root, temp, source, BIND)
            steps[0]['exitCode'] = 0
            with patch.object(source, 'source_identity', side_effect=ValueError('tracked harness changed')):
                with self.assertRaises(ValueError): control.admit(root, temp, source, BIND)
            self.assertFalse((temp / 'prior-red-admitted.json').exists())
