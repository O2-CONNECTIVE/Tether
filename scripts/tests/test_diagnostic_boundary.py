"""Synthetic-only validation; no producer, raw logs, or native calls."""
import importlib.util
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('diagnostic_boundary', ROOT / 'scripts/diagnostic_boundary.py')
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)
ENUMS = json.loads((ROOT / 'ci-producer/DIAGNOSTIC_ENUMS.json').read_text())
BASE = {'stage': 'complete', 'failure_code': 'none'}


def validate(record):
    return MODULE.validate_record(json.dumps(record).encode())


class DiagnosticBoundaryTests(unittest.TestCase):
    def test_synthetic_fixtures(self):
        cases = json.loads((ROOT / 'scripts/tests/fixtures/diagnostic-boundary/cases.json').read_text())
        self.assertGreater(len(cases), 0)
        for case in cases:
            with self.subTest(case=case['name']):
                result = validate(case['record'])
                self.assertEqual(result['validation'], case['validation'])
                self.assertNotIn('SYNTHETIC', json.dumps(result))
                self.assertNotIn('passed', json.dumps(result))
                if case['validation'] == 'unavailable':
                    self.assertEqual(result['fields'], {})

    def test_all_canonical_enums(self):
        for field, choices in MODULE.ALLOWED.items():
            for choice in choices:
                record = dict(BASE)
                for group in MODULE.GROUPS:
                    if field in group:
                        record.update(dict.fromkeys(group, 'unavailable'))
                record[field] = choice
                result = validate(record)
                self.assertEqual(result['validation'], 'record-valid', (field, choice))

    def test_unknown_and_wrong_types_at_every_field(self):
        for field in MODULE.ALLOWED:
            for value in ('SYNTHETIC-token', None, True, 3, [], {}):
                record = dict(BASE)
                for group in MODULE.GROUPS:
                    if field in group:
                        record.update(dict.fromkeys(group, 'unavailable'))
                record[field] = value
                self.assertEqual(validate(record)['validation'], 'unavailable')

    def test_each_missing_group_field_is_incomplete(self):
        for group in MODULE.GROUPS:
            for field in group:
                record = {**BASE, **dict.fromkeys(group, 'unavailable')}
                del record[field]
                self.assertEqual(validate(record)['reason'], 'incomplete-record')

    def test_malformed_inputs(self):
        for raw in (b'', b'{', b'null', b'[]', b'"SYNTHETIC"', b'\xff',
                    b'{"stage":"complete","stage":"complete","failure_code":"none"}',
                    b'{"stage":NaN}', b'[' * 1100 + b']' * 1100):
            result = MODULE.validate_record(raw)
            self.assertEqual(result['validation'], 'unavailable')
            self.assertNotIn('SYNTHETIC', json.dumps(result))

    def test_byte_limit_and_type(self):
        raw = json.dumps(BASE).encode()
        self.assertEqual(MODULE.validate_record(raw + b' ' * (MODULE.MAX_BYTES - len(raw)))['validation'], 'record-valid')
        self.assertEqual(MODULE.validate_record(raw + b' ' * (MODULE.MAX_BYTES + 1 - len(raw)))['reason'], 'oversized-record')
        self.assertEqual(MODULE.validate_record('SYNTHETIC')['reason'], 'invalid-record')

    def test_native_snapshot_requires_capture_and_recorder(self):
        record = {**BASE, **dict.fromkeys(ENUMS['native_fields'], 'unavailable')}
        record.update(native_first_group='owned_group_quiescent_snapshot', native_first_diag_signal_term='no')
        for capture in ('unavailable', 'invalid'):
            record['native_capture'] = capture
            fields = validate(record)['fields']
            self.assertEqual(fields['native_first_group'], 'unavailable')
            self.assertEqual(fields['native_first_diag_signal_term'], 'unavailable')
        record['native_capture'] = 'matched'
        fields = validate(record)['fields']
        self.assertEqual(fields['native_first_group'], 'owned_group_quiescent_snapshot')
        self.assertEqual(fields['native_first_diag_signal_term'], 'unavailable')
        record['native_first_diag_available'] = 'yes'
        self.assertEqual(validate(record)['fields']['native_first_diag_signal_term'], 'no')

    def test_no_input_mutation_or_cross_call_snapshot(self):
        first = validate({**BASE, **dict.fromkeys(ENUMS['result_fields'], 'unavailable')})
        first['fields']['stage'] = 'SYNTHETIC'
        self.assertEqual(validate(BASE)['fields'], BASE)
        self.assertEqual(BASE['stage'], 'complete')


if __name__ == '__main__':
    unittest.main()
