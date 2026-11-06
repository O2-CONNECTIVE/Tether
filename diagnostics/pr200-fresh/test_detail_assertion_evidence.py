"""Evidence-loss seam only; mocked fixture stdout is not a PowerShell cause reproduction."""
import tempfile
import copy
import hashlib
import json
import re
import contextlib
import io
import os
from unittest.mock import patch
from pathlib import Path
import unittest

import test_once_preflight as harness_module
import powershell_preflight as preflight
from test_phase2 import synthetic_preflight, identity
import phase2
import report


class DetailAssertionEvidenceTests(unittest.TestCase):
    def test_synthetic_known_assertion_survives_actual_preflight_collection(self):
        harness = harness_module.OncePreflightTests()
        with tempfile.TemporaryDirectory() as folder:
            result, value, calls = harness.execute_mock(Path(folder), 2,
                b'OBSERVER_DETAIL_ASSERTION_FAILED code=DETAIL_BOUND_FAILED\r\n',
                b'PRIVATE_EXCEPTION_PATH_STACK', 1)
            self.assertEqual(result, 1)
            self.assertEqual(len(calls), 3)
            self.assertFalse(value['passed'])
            self.assertEqual(value.get('detailAssertion'), {'status': 'known', 'code': 'DETAIL_BOUND_FAILED', 'case': None, 'comparison': None, 'actualDetail': None})

    def test_synthetic_unknown_missing_conflicting_and_truncated_output_stays_red(self):
        marker = b'OBSERVER_DETAIL_ASSERTION_FAILED code=DETAIL_BOUND_FAILED'
        cases = [(b'', 'missing'), (b'\r\n', 'missing'), (b'PRIVATE_EXCEPTION_PATH_STACK', 'unknown'),
            (b'OBSERVER_DETAIL_ASSERTION_FAILED code=PRIVATE_UNKNOWN', 'unknown'),
            (marker + b'\nPRIVATE_EXTRA', 'unknown'), (marker + b'\n' + marker, 'conflicting'),
            (marker + b'\nOBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_VERDICT_CHANGED', 'conflicting'),
            (marker + b'\nOBSERVER_DETAIL_SYNTHETIC_PASS', 'conflicting'),
            (b'OBSERVER_DETAIL_ASSERTION_FAILED code=DETAIL_', 'unknown'),
            (b'\xff', 'unknown'), (b'x' * 4097, 'truncated')]
        harness = harness_module.OncePreflightTests()
        for stdout, expected in cases:
            with self.subTest(expected=expected), tempfile.TemporaryDirectory() as folder:
                result, value, calls = harness.execute_mock(Path(folder), 2, stdout,
                    b'PRIVATE_STDERR_OBSERVER_DETAIL_ASSERTION_FAILED code=DETAIL_BOUND_FAILED', 1)
                self.assertEqual(result, 1); self.assertEqual(len(calls), 3)
                self.assertFalse(preflight.current_passed(value))
                self.assertEqual(value['detailAssertion'], {'status': expected, 'code': None, 'case': None, 'comparison': None, 'actualDetail': None})
                self.assertNotIn('PRIVATE_', json.dumps(value))
                self.assertEqual(value['fixturesVerified'], 1)
        with tempfile.TemporaryDirectory() as folder:
            result, value, _ = harness.execute_mock(Path(folder), 2, marker, b'', 0)
            self.assertEqual(result, 1)
            self.assertEqual(value['detailAssertion'], {'status': 'conflicting', 'code': None, 'case': None, 'comparison': None, 'actualDetail': None})

    def test_synthetic_known_assertion_reaches_complete_controlled_publication_without_raw_logs(self):
        harness = harness_module.OncePreflightTests()
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            result, value, _ = harness.execute_mock(root, 2,
                b'OBSERVER_DETAIL_ASSERTION_FAILED code=DETAIL_BOUND_FAILED\n', b'PRIVATE_EXCEPTION', 1)
            self.assertEqual(result, 1)
            report.write(root / 'safe/build-identity.json', identity())
            output = io.StringIO()
            with patch.dict(os.environ, GITHUB_STEP_SUMMARY=str(root / 'summary.md')), contextlib.redirect_stdout(output):
                self.assertEqual(phase2.publish(root, 1), 0)
            record = json.loads('\n'.join(output.getvalue().splitlines()[1:-1]))
            self.assertEqual(record['files']['powershell-preflight.json'], value)
            self.assertEqual(len(record['files']), 2)
            self.assertNotIn('PRIVATE_', output.getvalue()); self.assertNotIn(str(root), output.getvalue())
            self.assertEqual(value['detailAssertion'], {'status': 'known', 'code': 'DETAIL_BOUND_FAILED', 'case': None, 'comparison': None, 'actualDetail': None})

    def test_synthetic_all_known_assertions_are_closed_and_never_turn_failure_green(self):
        harness = harness_module.OncePreflightTests()
        for code in preflight.ASSERTION_CODES:
            with tempfile.TemporaryDirectory() as folder:
                result, value, _ = harness.execute_mock(Path(folder), 2,
                    ('OBSERVER_DETAIL_ASSERTION_FAILED code=' + code + (' case=getter-exception comparison=new-detail actualDetail=metadata-command-line-guard' if code == 'METADATA_SHORT_CIRCUIT_CHANGED' else '') + '\n').encode(), b'PRIVATE_ERROR', 1)
                self.assertEqual(result, 1)
                self.assertEqual(value['detailAssertion'], {'status': 'known', 'code': code, 'case': 'getter-exception' if code == 'METADATA_SHORT_CIRCUIT_CHANGED' else None, 'comparison': 'new-detail' if code == 'METADATA_SHORT_CIRCUIT_CHANGED' else None, 'actualDetail': 'metadata-command-line-guard' if code == 'METADATA_SHORT_CIRCUIT_CHANGED' else None})
                self.assertFalse(preflight.current_passed(value))

    def test_synthetic_missing_unknown_or_conflicting_classification_cannot_authorize_pass(self):
        for change in [None, {'status': 'known', 'code': 'PRIVATE_VALUE'},
            {'status': 'missing', 'code': None}, {'status': 'unknown', 'code': None},
            {'status': 'conflicting', 'code': None, 'case': None, 'comparison': None, 'actualDetail': None}, {'status': 'truncated', 'code': None},
            {'status': 'known', 'code': 'DETAIL_BOUND_FAILED', 'case': None, 'comparison': None, 'actualDetail': None},
            {'status': 'not-applicable', 'code': None, 'path': 'PRIVATE_PATH'}]:
            value = synthetic_preflight()
            if change is None: del value['detailAssertion']
            else: value['detailAssertion'] = change
            with self.assertRaises(ValueError): preflight.current_passed(value)
        value = synthetic_preflight(); value['schema'] = 2; del value['detailAssertion']
        self.assertEqual(preflight.preflight_safe(value), value)
        self.assertFalse(preflight.current_passed(value))

    def test_real_pr213_record_retains_unknown_assertion_without_backfill(self):
        root = Path(__file__).resolve().parent
        record = json.loads((root / 'fixtures/pr213-preflight-record.json').read_text())
        original = copy.deepcopy(record)
        canonical = json.dumps(record, sort_keys=True, separators=(',', ':')).encode()
        self.assertEqual(len(canonical), 1551)
        self.assertEqual(hashlib.sha256(canonical).hexdigest(), 'd219204d88fc45cfcb2053a60051ebcd582770dfb7aceaf9c8c9d8f2ffc0d152')
        self.assertEqual(len(record['files']), 2)
        for name, value in record['files'].items():
            self.assertEqual(hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest(), record['fileSha256'][name])
        value = record['files']['powershell-preflight.json']
        self.assertEqual(preflight.preflight_safe(value), value)
        self.assertNotIn('detailAssertion', value)
        self.assertFalse(preflight.current_passed(value))
        self.assertEqual(value['stages']['detail']['exitCode'], 1)
        self.assertFalse(value['passed']); self.assertEqual(record, original)

    def test_static_base_logic_is_preserved_outside_grounded_member_boundary_repair(self):
        # No PowerShell interpreter involved. Remove only the new literal writes;
        # exact old normalized source must remain, including all assertion logic.
        root = Path(__file__).resolve().parent
        from test_member_boundary_repair import without_boundary_repair
        text = without_boundary_repair((root / 'observer-detail.test.ps1').read_text())
        begin = text.index("try {\n        $metadataActualDetailCode = 'not-evaluated'")
        finish = text.index("} catch { };", begin) + len("} catch { };")
        text = text[:begin] + "try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_SHORT_CIRCUIT_CHANGED') } catch { };" + text[finish:]
        text = text.replace('($metadataActualDetail = $new.detail)', '$new.detail')
        for label in preflight.METADATA_CASES:
            text = text.replace(", '" + label + "')", ")")
        for label, term in [('old-calls', '$old.calls -ne 1'), ('new-calls', '$new.calls -ne 1'),
            ('old-reads', '$old.reads -ne $case[4]'), ('new-reads', '$new.reads -ne $case[4]'), ('new-detail', '$new.detail -cne $case[3]')]:
            text = text.replace("(($metadataComparison = '" + label + "') -and (" + term + "))", term)
        pattern = r"try \{ \[Console\]::Out\.WriteLine\('OBSERVER_DETAIL_ASSERTION_FAILED code=([A-Z_]+)'\) \} catch \{ \}; "
        markers = re.findall(pattern, text)
        self.assertEqual(len(markers), 14)
        self.assertEqual(set(markers), preflight.ASSERTION_CODES - {'METADATA_BOUNDARY_CONTROL_FAILED'})
        projected = re.sub(pattern, '', text)
        self.assertEqual(hashlib.sha256(projected.encode()).hexdigest(), '0deeb8e28cb31a3ba6e46ce20940aba09e8f9e3cffd90b5be88dbb2455f51a1f')
        for code in markers:
            self.assertIn("code=" + code + "') } catch { }; throw '" + code + "'", text)
        self.assertNotIn('OBSERVER_DETAIL_ASSERTION_FAILED code=$', text)


if __name__ == '__main__':
    unittest.main()
