"""Retained-evidence replay/source projection tests; NOT a Windows reproduction."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT / 'scripts'))
import apply_driver_trace as original
from verify_driver_trace import inspect

spec = importlib.util.spec_from_file_location('prepare_forward_fixture', HERE / 'prepare_fixture.py')
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


def encoded(report):
    rows = [{'type': 'header', 'schema': 1, 'variant': 'tauri-driver-2.1.0-stage-1'},
            *report['events'], report['summary']]
    return '\n'.join(json.dumps(row, separators=(',', ':')) for row in rows).encode()


class SourceTests(unittest.TestCase):
    def test_cli_rejection_never_echoes_paths_arguments_or_exceptions(self):
        cases = [['--PRIVATE_UNKNOWN', 'PRIVATE_VALUE'],
                 ['--archive', 'PRIVATE_MISSING_ARCHIVE', '--output', 'PRIVATE_OUTPUT']]
        for args in cases:
            result = subprocess.run([sys.executable, '-B', str(HERE / 'prepare_fixture.py'), *args],
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stdout, 'FORWARD_FIXTURE_SOURCE_REJECTED\n')
            self.assertEqual(result.stderr, '')

    def test_actual_retained_reports_replay_with_original_verifier(self):
        manifest = json.loads((HERE / 'records.json').read_text())
        self.assertEqual(set(manifest), {'pr200-failure.json', 'pr231-failure.json', 'pr231-success.json'})
        for name, digest in manifest.items():
            raw = (HERE / 'records' / name).read_bytes()
            self.assertEqual(hashlib.sha256(raw).hexdigest(), digest)
            data = json.loads(raw)
            trace = data['driver-stage-report.json']
            self.assertEqual(inspect(encoded(trace)), trace)
            observation = data['native-observation.json']
            result = [r for r in observation['requests']
                      if r['operation'] == 'create-session' and r['outcome'] != 'started']
            self.assertEqual(len(result), 1)
            if 'failure' in name:
                self.assertEqual(trace['requestStage'], 'dropped-before-headers')
                self.assertEqual(observation['phase'], 'session-create')
                self.assertEqual(result[0]['outcome'], 'timeout')
                self.assertNotIn('status', result[0])
                self.assertEqual(observation['documentObservations'], 0)
            else:
                self.assertEqual(trace['requestStage'], 'native-headers')
                self.assertEqual(result[0]['outcome'], 'ok')
                self.assertEqual(result[0]['status'], 200)

    def test_synthetic_missing_overflow_unknown_field_stay_unconfirmed(self):
        saved = json.loads((HERE / 'records/pr231-failure.json').read_text())['driver-stage-report.json']
        damaged = copy.deepcopy(saved)
        damaged['summary']['dropped'] = 1
        self.assertEqual(inspect(encoded(damaged))['requestStage'], 'unconfirmed')
        damaged = copy.deepcopy(saved)
        damaged['events'][4]['PRIVATE_UNKNOWN'] = 'SYNTHETIC_SECRET'
        with self.assertRaises(ValueError):
            inspect(encoded(damaged))
        damaged = copy.deepcopy(saved)
        damaged['events'] = damaged['events'][:-1]
        self.assertEqual(inspect(encoded(damaged))['diagnostic'], 'incomplete')

    def test_pinned_projection_preserves_real_handler_except_capture(self):
        archive = Path(os.environ['SESSION_FORWARD_ARCHIVE'])
        upstream = original.upstream_files(archive.read_bytes())
        patch, code, expected = original.bundle(ROOT / 'tools/native-driver-trace')
        baseline = original.apply_exact(upstream, patch)
        baseline.update({'src/' + n: b for n, b in code.items()})
        self.assertTrue(all(original.sha(baseline[n]) == h for n, h in expected.items()))
        candidate = original.apply_exact(baseline, (HERE / 'connection.patch').read_bytes())
        changed = {n for n in candidate if candidate[n] != baseline[n]}
        self.assertEqual(changed, {'src/server.rs', 'src/diagnostic.rs'})
        addition = b'''  if let Some(trace) = trace.as_mut().filter(|trace| trace.capture_enabled()) {
    trace.capture(hyper_util::client::legacy::connect::capture_connection(&mut forwarded));
  }
'''
        self.assertEqual(candidate['src/server.rs'].count(addition), 1)
        restored = candidate['src/server.rs'].replace(addition, b'').replace(
            b'let mut forwarded =', b'let forwarded =')
        self.assertEqual(restored, baseline['src/server.rs'])
        self.assertIn(b'pub fn capture_enabled(&self) -> bool {\n        self.connection.recorder.is_some()\n    }', candidate['src/diagnostic.rs'])
        self.assertNotIn(b'wait_for_connection_metadata', candidate['src/diagnostic.rs'])
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'fixture'
            prepare.prepare(archive, target)
            self.assertEqual((target / 'Cargo.lock').read_bytes(), upstream['Cargo.lock'])
            metadata = json.loads((target / 'projection.json').read_text())
            self.assertTrue(metadata['cargoLockUnchanged'])
            self.assertEqual(metadata['sourceSha256']['src/server.rs'], original.sha(candidate['src/server.rs']))
            with self.assertRaises(original.PatchError):
                prepare.prepare(archive, target)

    def test_wrong_archive_and_missing_capture_cannot_pass_source_projection(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'wrong.crate'
            path.write_bytes(b'SYNTHETIC_NOT_UPSTREAM')
            with self.assertRaises(original.PatchError):
                prepare.prepare(path, Path(directory) / 'fixture')
        archive = Path(os.environ['SESSION_FORWARD_ARCHIVE'])
        upstream = original.upstream_files(archive.read_bytes())
        patch, code, _ = original.bundle(ROOT / 'tools/native-driver-trace')
        baseline = original.apply_exact(upstream, patch)
        baseline.update({'src/' + n: b for n, b in code.items()})
        baseline['src/server.rs'] += b'\nSYNTHETIC_DRIFT\n'
        candidate = original.apply_exact(baseline, (HERE / 'connection.patch').read_bytes())
        expected = json.loads((HERE / 'postimage.json').read_text())
        self.assertNotEqual(original.sha(candidate['src/server.rs']), expected['src/server.rs'])


if __name__ == '__main__':
    unittest.main()
