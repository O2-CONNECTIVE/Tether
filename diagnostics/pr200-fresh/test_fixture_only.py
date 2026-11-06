"""Local recorder/protocol mocks and static scope checks; no PowerShell/native execution."""
import contextlib
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import fixture_only as recorder
import powershell_preflight
from test_phase2 import synthetic_preflight as current_preflight

ROOT = Path(__file__).resolve().parent


def synthetic_preflight():
    # This older recorder is pinned to the historical43f schema3 source.
    value = current_preflight()
    value['schema'] = 3
    del value['detailAssertion']['case']; del value['detailAssertion']['comparison']; del value['detailAssertion']['actualDetail']
    return value


def identity():
    return {'schema': 1, 'controlCommit': 'a' * 40, 'controlTree': 'b' * 40,
        'fixtureSourceCommit': recorder.SOURCE, 'fixtureSourceTree': recorder.TREE,
        'reviewedPatchSha256': recorder.PATCH, 'sourceVerified': True, 'sourceStableAfter': True,
        'sourceFileSha256': {key: 'c' * 64 for key in recorder.FILES}, 'runnerLabel': 'windows-2022',
        'pythonVersion': '3.13.17', 'powershellVersion': '7.5.4'}


class FixtureOnlyTests(unittest.TestCase):
    def observe(self, root, failed=False, changes=None, drift=False):
        calls = []
        def preflight(source, target):
            calls.append('preflight')
            data = synthetic_preflight()
            if failed:
                data.update(passed=False, fixturesVerified=1, detailAssertion={'status': 'known', 'code': 'METADATA_SHORT_CIRCUIT_CHANGED'})
                data['stages']['detail'].update(passed=False, exitCode=1, code='fixture-rejected')
            (target / 'safe').mkdir()
            (target / 'safe/powershell-preflight.json').write_text(json.dumps(data))
            print('PRIVATE_UNPUBLISHED_RECORDER_OUTPUT')
            return 1 if failed else 0
        module = SimpleNamespace(preflight=preflight, preflight_safe=powershell_preflight.preflight_safe)
        def git(path, *args):
            if args == ('rev-parse', 'HEAD'): return ('a' * 40).encode()
            if args == ('rev-parse', 'HEAD^{tree}'): return ('b' * 40).encode()
            return b''
        env = {'RUNNER_ENVIRONMENT': 'github-hosted', 'RUNNER_OS': 'Windows', 'RUNNER_ARCH': 'X64',
            'EXPECTED_CONTROL': 'a' * 40, 'RECORDER_POWERSHELL_VERSION': '7.5.4', 'RECORDER_PS_PATH_MATCH': 'true',
            'GITHUB_STEP_SUMMARY': str(root / 'summary.md')}
        env.update(changes or {})
        hashes = identity()['sourceFileSha256']
        after = {key: 'd' * 64 for key in hashes} if drift else hashes
        output = io.StringIO()
        with patch.object(recorder.sys, 'platform', 'win32'), patch.object(recorder.sys, 'version_info', (3, 13, 17)), \
             patch.dict(os.environ, env), patch.object(recorder, 'git', side_effect=git), \
             patch.object(recorder, 'source_hashes', side_effect=[hashes, after]), \
             patch.object(recorder, 'load_preflight', return_value=module) as load, contextlib.redirect_stdout(output):
            result = recorder.observe(root / 'source', root / 'control', root / 'observation')
        text = output.getvalue()
        record = json.loads('\n'.join(text.splitlines()[1:-1]))
        raw = json.dumps(record, sort_keys=True, separators=(',', ':')).encode()
        self.assertEqual(text.splitlines()[0], 'BEGIN_PR213_FIXTURE_RECORD sha256:' + hashlib.sha256(raw).hexdigest())
        self.assertEqual(text.splitlines()[-1], 'END_PR213_FIXTURE_RECORD')
        self.assertNotIn('PRIVATE_', text); self.assertNotIn(str(root), text)
        self.assertIn(text, (root / 'summary.md').read_text())
        for name, value in record['files'].items():
            self.assertEqual(hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest(), record['fileSha256'][name])
        return result, record, calls, load.call_count

    def test_synthetic_pass_and_failure_invoke_fixed_chain_once_and_publish_full_safe_record(self):
        for failed in [False, True]:
            with tempfile.TemporaryDirectory() as folder:
                result, record, calls, loads = self.observe(Path(folder), failed)
                self.assertEqual(result, int(failed)); self.assertEqual(calls, ['preflight']); self.assertEqual(loads, 1)
                self.assertEqual(record['outcome'], 'fixture-failed' if failed else 'fixture-passed')
                self.assertEqual(set(record['files']), {'identity.json', 'powershell-preflight.json'})
                self.assertTrue(record['fixtureOnly']); self.assertTrue(record['pr200Blocked'])
                if failed:
                    self.assertEqual(record['files']['powershell-preflight.json']['detailAssertion']['code'], 'METADATA_SHORT_CIRCUIT_CHANGED')

    def test_synthetic_unknown_runtime_or_pin_stops_before_loading_or_execution(self):
        for changes in [{'RECORDER_POWERSHELL_VERSION': 'PRIVATE_PATH'}, {'RECORDER_PS_PATH_MATCH': 'false'},
                        {'EXPECTED_CONTROL': 'd' * 40}, {'RUNNER_ENVIRONMENT': 'self-hosted'}]:
            with tempfile.TemporaryDirectory() as folder:
                result, record, calls, loads = self.observe(Path(folder), changes=changes)
                self.assertEqual(result, 1); self.assertEqual(calls, []); self.assertEqual(loads, 0)
                self.assertEqual(record['outcome'], 'boundary-rejected')

    def test_synthetic_source_drift_after_single_execution_cannot_be_a_pass(self):
        with tempfile.TemporaryDirectory() as folder:
            result, record, calls, _ = self.observe(Path(folder), drift=True)
            self.assertEqual(result, 1); self.assertEqual(calls, ['preflight'])
            self.assertEqual(record['outcome'], 'recorder-rejected')
            self.assertFalse(record['files']['identity.json']['sourceStableAfter'])

    def test_synthetic_source_pin_only_accepts_exact_git_blob_or_crlf_variant(self):
        for data, expected in [(b'fixture\n', True), (b'fixture\r\n', True), (b'PRIVATE_CHANGED', False)]:
            with tempfile.TemporaryDirectory() as folder:
                root = Path(folder); target = root / 'diagnostics/pr200-fresh'; target.mkdir(parents=True)
                for name in recorder.FILES.values(): (target / name).write_bytes(data)
                def git(path, *args):
                    if args == ('rev-parse', 'HEAD'): return recorder.SOURCE.encode()
                    if args == ('rev-parse', 'HEAD^{tree}'): return recorder.TREE.encode()
                    if args[0] == 'show': return b'fixture\n'
                    return b''
                with patch.object(recorder, 'git', side_effect=git):
                    if expected:
                        self.assertEqual(set(recorder.source_hashes(root)), set(recorder.FILES))
                    else:
                        with self.assertRaises(ValueError): recorder.source_hashes(root)

    def test_unknown_identity_fields_reject_before_any_publication(self):
        for update in [{'path': 'PRIVATE_PATH'}, {'powershellVersion': 'PRIVATE_VERSION'},
                       {'sourceFileSha256': {'PRIVATE_KEY': 'c' * 64}}, {'controlTree': 'PRIVATE_TREE'}]:
            value = copy.deepcopy(identity()); value.update(update)
            with self.assertRaises(ValueError): recorder.identity_safe(value)

    def test_real_fresh_python_import_seam_loads_only_fixed_helper_definitions(self):
        code = 'import sys; from pathlib import Path; sys.path.insert(0, sys.argv[1]); import fixture_only; fixture_only.load_preflight(Path(sys.argv[2])); print("FIXED_HELPER_IMPORT_PASS")'
        result = subprocess.run([sys.executable, '-B', '-c', code, str(ROOT), str(ROOT.parents[1])],
                                capture_output=True, text=True, timeout=10, check=True)
        self.assertEqual(result.stdout.strip(), 'FIXED_HELPER_IMPORT_PASS'); self.assertEqual(result.stderr, '')

    def test_static_workflow_has_only_checkout_python_and_one_fixture_recorder(self):
        text = (ROOT.parents[1] / '.github/workflows/pr213-fixture-only.yml').read_text()
        self.assertEqual(text.count('      - name:'), 4)
        self.assertEqual(text.count('uses: actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0'), 2)
        self.assertEqual(text.count('uses: actions/setup-python@5fda3b95a4ea91299a34e894583c3862153e4b97'), 1)
        self.assertEqual(text.count('python -B control/diagnostics/pr200-fresh/fixture_only.py'), 1)
        self.assertIn('types: [opened]', text); self.assertIn('github.run_attempt == 1', text)
        for forbidden in ['npm', 'cargo', 'rustup', 'setup-node', 'native-smoke', 'phase2.py', 'workflow_dispatch',
                          'workflow_call', 'upload-artifact', 'cache:', 'matrix:', 'secrets.']:
            self.assertNotIn(forbidden, text)


if __name__ == '__main__':
    unittest.main()
