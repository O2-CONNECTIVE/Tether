"""Refinement protocol seam, not a recreation of the PowerShell runtime case."""
import tempfile
import copy
import hashlib
import json
import re
from pathlib import Path
import unittest

import test_once_preflight as harness_module
import powershell_preflight as preflight
from test_phase2 import synthetic_preflight


class CaseComparisonTests(unittest.TestCase):
    def test_synthetic_actual_collector_retains_case_and_first_comparison(self):
        harness = harness_module.OncePreflightTests()
        with tempfile.TemporaryDirectory() as folder:
            result, value, calls = harness.execute_mock(Path(folder), 2,
                b'OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_SHORT_CIRCUIT_CHANGED case=getter-exception comparison=new-detail actualDetail=metadata-command-line-guard\r\n',
                b'PRIVATE_EXCEPTION', 1)
            self.assertEqual(result, 1); self.assertEqual(len(calls), 3)
            self.assertEqual(value['detailAssertion'], {'status': 'known', 'code': 'METADATA_SHORT_CIRCUIT_CHANGED',
                'case': 'getter-exception', 'comparison': 'new-detail', 'actualDetail': 'metadata-command-line-guard'})

    def test_synthetic_every_closed_case_comparison_and_unknown_protocol_stays_failed(self):
        harness = harness_module.OncePreflightTests()
        for case in preflight.METADATA_CASES:
            for comparison in preflight.METADATA_COMPARISONS:
                with tempfile.TemporaryDirectory() as folder:
                    raw = f'OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_SHORT_CIRCUIT_CHANGED case={case} comparison={comparison} actualDetail={"metadata-command-line-guard" if comparison == "new-detail" else "not-evaluated"}\n'.encode()
                    result, value, _ = harness.execute_mock(Path(folder), 2, raw, b'PRIVATE_ERROR', 1)
                    self.assertEqual(result, 1)
                    self.assertEqual(value['detailAssertion'], {'status': 'known', 'code': 'METADATA_SHORT_CIRCUIT_CHANGED', 'case': case, 'comparison': comparison, 'actualDetail': 'metadata-command-line-guard' if comparison == 'new-detail' else None})
                    self.assertFalse(preflight.current_passed(value))
        prefix = b'OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_SHORT_CIRCUIT_CHANGED'
        for raw in [prefix, prefix + b' case=PRIVATE_CASE comparison=new-detail',
                    prefix + b' case=getter-exception comparison=PRIVATE_VALUE', prefix + b' case=getter-exception',
                    prefix + b' comparison=new-detail case=getter-exception',
                    b'OBSERVER_DETAIL_ASSERTION_FAILED code=DETAIL_BOUND_FAILED case=getter-exception comparison=new-detail']:
            value = preflight.assertion_output(raw)
            self.assertEqual(value, {'status': 'unknown', 'code': None, 'case': None, 'comparison': None, 'actualDetail': None})
            self.assertNotIn('PRIVATE_', json.dumps(value))
        valid = prefix + b' case=getter-exception comparison=new-detail actualDetail=metadata-command-line-guard'
        self.assertEqual(preflight.assertion_output(valid + b'\n' + valid)['status'], 'conflicting')
        self.assertEqual(preflight.assertion_output(valid + b'x' * 4097)['status'], 'truncated')

    def test_synthetic_actual_detail_is_only_allowlisted_when_original_last_term_was_evaluated(self):
        prefix = b'OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_SHORT_CIRCUIT_CHANGED case=getter-exception comparison='
        for detail in preflight.METADATA_DETAILS:
            value = preflight.assertion_output(prefix + b'new-detail actualDetail=' + detail.encode())
            self.assertEqual(value['actualDetail'], detail)
            self.assertEqual(value['status'], 'known')
        for raw in [prefix + b'new-detail actualDetail=PRIVATE_RAW', prefix + b'new-detail actualDetail=not-evaluated',
                    prefix + b'old-reads actualDetail=metadata-command-line-guard', prefix + b'new-detail actualDetail=']:
            value = preflight.assertion_output(raw)
            self.assertEqual(value, {'status': 'unknown', 'code': None, 'case': None, 'comparison': None, 'actualDetail': None})
            self.assertNotIn('PRIVATE_', json.dumps(value))

    def test_synthetic_refinement_absence_or_inconsistent_codes_never_authorizes_pass(self):
        for assertion in [
            {'status': 'known', 'code': 'METADATA_SHORT_CIRCUIT_CHANGED', 'case': None, 'comparison': None, 'actualDetail': None},
            {'status': 'known', 'code': 'DETAIL_BOUND_FAILED', 'case': 'getter-exception', 'comparison': 'new-detail'},
            {'status': 'not-applicable', 'code': None, 'case': 'getter-exception', 'comparison': None},
            {'status': 'not-applicable', 'code': None}]:
            value = synthetic_preflight(); value['detailAssertion'] = assertion
            with self.assertRaises(ValueError): preflight.current_passed(value)
        legacy = synthetic_preflight(); legacy['schema'] = 3
        del legacy['detailAssertion']['case']; del legacy['detailAssertion']['comparison']; del legacy['detailAssertion']['actualDetail']
        self.assertEqual(preflight.preflight_safe(legacy), legacy)
        self.assertFalse(preflight.current_passed(legacy))

    def test_real_pr215_record_does_not_gain_unretained_case_or_comparison(self):
        root = Path(__file__).resolve().parent
        record = json.loads((root / 'fixtures/pr215-fixture-record.json').read_text())
        original = copy.deepcopy(record)
        raw = json.dumps(record, sort_keys=True, separators=(',', ':')).encode()
        self.assertEqual(len(raw), 1987)
        self.assertEqual(hashlib.sha256(raw).hexdigest(), 'a55b722cef63328204f4685d1982d01b1c6973191086746936c544bff67d6c19')
        self.assertEqual(len(record['files']), 2)
        for name, value in record['files'].items():
            self.assertEqual(hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest(), record['fileSha256'][name])
        value = record['files']['powershell-preflight.json']
        self.assertEqual(preflight.preflight_safe(value), value)
        self.assertEqual(value['detailAssertion'], {'status': 'known', 'code': 'METADATA_SHORT_CIRCUIT_CHANGED'})
        self.assertFalse(preflight.current_passed(value)); self.assertEqual(record, original)

    def test_static_first_failure_labels_wrap_same_predicates_in_original_order(self):
        text = (Path(__file__).resolve().parent / 'observer-detail.test.ps1').read_text()
        pairs = re.findall(r"\(\(\$metadataComparison = '([^']+)'\) -and \(([^\n]+?)\)\)", text)
        self.assertEqual(pairs, [('old-calls', '$old.calls -ne 1'), ('new-calls', '$new.calls -ne 1'),
            ('old-reads', '$old.reads -ne $case[4]'), ('new-reads', '$new.reads -ne $case[4]'), ('new-detail', '($metadataActualDetail = $new.detail) -cne $case[3]')])
        table = text.split("$script:sentinel = [Exception]::new('SYNTHETIC_ERROR_NOT_PUBLISHED')", 1)[1].split(')) {', 1)[0]
        for case in preflight.METADATA_CASES:
            self.assertEqual(table.count(", '" + case + "')"), 1)
        self.assertIn("case={0} comparison={1} actualDetail={2}' -f $case[5], $metadataComparison, $metadataActualDetailCode", text)
        self.assertEqual(text.count('$new.detail'), 1)
        whitelist = text.split('$metadataActualDetail -cin @(', 1)[1].split(')', 1)[0]
        self.assertEqual(set(re.findall(r"'([^']+)'", whitelist)), preflight.METADATA_DETAILS - {'unclassified-detail'})
        self.assertIn('if ($metadataComparison -ceq \'new-detail\')', text)



if __name__ == '__main__':
    unittest.main()
