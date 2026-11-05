"""Real retained classifications and synthetic refinement contracts; no PowerShell execution."""
import copy
import hashlib
import json
from pathlib import Path
import re
import unittest
from unittest.mock import patch

import evidence_schema as schema
import phase2
from test_phase2 import capability, reports, synthetic_classification
from test_profile_contract import valid

ROOT = Path(__file__).resolve().parent
REPO = ROOT.parents[1]


class PredicateRefinementTests(unittest.TestCase):
    def test_real_pr211_record_preserves_discarded_predicate_and_guard_unknowns(self):
        record = json.loads((ROOT / 'fixtures/pr211-trial1-record.json').read_text())
        original = copy.deepcopy(record)
        canonical = json.dumps(record, sort_keys=True, separators=(',', ':')).encode()
        self.assertEqual(len(canonical), 28488)
        self.assertEqual(hashlib.sha256(canonical).hexdigest(), 'e922423db73c2743e04a5a0ebc32b095b62392f4019d3a4346e3e8929d319dd3')
        self.assertEqual(len(record['files']), 15)
        for name, value in record['files'].items():
            self.assertEqual(hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest(), record['fileSha256'][name])
        self.assertEqual(schema.retained_classification(record), {
            'capabilityClass': 'candidate-invalid-path', 'observerSites': ['lineage-query', 'webview-metadata'],
            'pathPredicate': 'not-retained', 'observerDetails': 'not-retained'})
        files = record['files']; trial = files['trial.json']
        self.assertTrue(trial['classificationComplete'])  # Original meaning is not rewritten.
        self.assertFalse(trial['continue']); self.assertEqual(trial['sessionElapsedMs'], 12063)
        self.assertEqual(files['observer-failure-sites.json']['failureCount'], 8)
        diagnostics = {key: files[key] for key in ['profile-capability-shape.json', 'observer-failure-sites.json']}
        self.assertFalse(schema.classification_complete(diagnostics, files['profile-observation.json'], files['profile-capability.json']))
        self.assertEqual(record, original)

    def test_synthetic_old_schema_missing_predicate_unknown_detail_cannot_continue(self):
        for mutate in [
            lambda d: (d['profile-capability-shape.json'].update(schema=1), d['profile-capability-shape.json'].pop('pathPredicate')),
            lambda d: (d['observer-failure-sites.json'].update(schema=1), d['observer-failure-sites.json'].pop('details')),
            lambda d: d['profile-capability-shape.json'].update(pathPredicate='not-applicable'),
        ]:
            data = synthetic_classification(); mutate(data)
            self.assertFalse(phase2.decision([0, 0, 0], reports(), valid(), capability(), data)['continue'])
        data = synthetic_classification(); profile = valid(); profile['queryComplete'] = False
        data['observer-failure-sites.json'].update(failureCount=1, sites=['lineage-query'],
            details=[{'site': 'lineage-query', 'code': 'unclassified-detail'}])
        self.assertFalse(schema.classification_complete(data, profile, capability()))

    def test_synthetic_predicates_only_classify_existing_rejection_never_authorize_profile(self):
        for predicate in schema.PATH_REJECTIONS:
            data = synthetic_classification(); cap = capability(); cap.update(capabilityObserved=False)
            data['profile-capability-shape.json'].update(candidateClass='candidate-invalid-path', pathPredicate=predicate)
            self.assertTrue(schema.classification_complete(data, valid(), cap))
            self.assertFalse(phase2.decision([0, 0, 0], reports(), valid(), cap, data)['continue'])
        data['profile-capability-shape.json']['pathPredicate'] = 'accepted'
        self.assertFalse(schema.classification_complete(data, valid(), cap))

    def test_synthetic_detail_pairing_bounds_unknown_fields_and_redaction_are_closed(self):
        for detail in [{'site': 'lineage-query', 'code': 'PRIVATE_EXCEPTION'},
                       {'site': 'lineage-query', 'code': 'metadata-creation-guard'},
                       {'site': 'PRIVATE_PATH', 'code': 'operation-exception'},
                       {'site': 'lineage-query', 'code': 'lineage-parent-row-guard-site', 'text': 'PRIVATE'}]:
            value = synthetic_classification()['observer-failure-sites.json']
            value.update(failureCount=1, sites=['lineage-query'], details=[detail])
            with self.assertRaises(ValueError): schema.observer_sites_safe(value)
        for detail in schema.LINEAGE_DETAILS:
            value = synthetic_classification()['observer-failure-sites.json']
            value.update(failureCount=1, sites=['lineage-query'], details=[{'site': 'lineage-query', 'code': detail}])
            self.assertEqual(schema.observer_sites_safe(value), value)
        for mutate in [lambda x: x.update(details=[]), lambda x: x.update(details=x['details'] * 17),
                       lambda x: x.update(path='PRIVATE')]:
            mutate(value)
            with self.assertRaises(ValueError): schema.observer_sites_safe(value)
            value = synthetic_classification()['observer-failure-sites.json']
            value.update(failureCount=1, sites=['lineage-query'], details=[{'site': 'lineage-query', 'code': 'lineage-parent-row-guard-site'}])
        shape = synthetic_classification()['profile-capability-shape.json']; shape['pathPredicate'] = 'PRIVATE_PATH'
        with self.assertRaises(ValueError): schema.capability_shape_safe(shape)

    def test_synthetic_crlf_checkout_satisfies_same_exact_source_pin_contract(self):
        native = REPO / 'ui/tauri/native-smoke/processes.ps1'
        read_bytes = Path.read_bytes
        crlf = read_bytes(native).replace(b'\r\n', b'\n').replace(b'\n', b'\r\n')
        def read(path):
            return crlf if path == native else read_bytes(path)
        with patch.object(Path, 'read_bytes', read):
            self.test_static_native_source_pin_and_location_map_match_frozen_statements()

    def test_static_native_source_pin_and_location_map_match_frozen_statements(self):
        # Static contract, explicitly not a PowerShell runtime/stack-format proof.
        native = REPO / 'ui/tauri/native-smoke/processes.ps1'
        actual = native.read_bytes()
        lf = actual.replace(b'\r\n', b'\n')
        digest = hashlib.sha256(lf).hexdigest()
        self.assertEqual(digest, 'a54b23fea4ed7bfc951701f2c2bee50bd3d414ca850915d5b01626670f086303')
        observer = (ROOT / 'observe-profile.ps1').read_text()
        self.assertIn(digest, observer)
        crlf = lf.replace(b'\n', b'\r\n')
        self.assertIn(actual, (lf, crlf))
        crlf_digest = hashlib.sha256(crlf).hexdigest()
        self.assertEqual(crlf_digest, '8667b375c8ba70aa06b36031888449ea796092cea2868bda16bf8709369bd72f')
        pins = re.search(r'\.Hash -in @\(([^)]+)\)', observer).group(1)
        self.assertEqual(set(re.findall(r"'([0-9a-f]{64})'", pins)), {digest, crlf_digest})
        self.assertEqual(lf.splitlines(), crlf.splitlines())
        self.assertNotIn(hashlib.sha256(native.read_bytes() + b' ').hexdigest(), pins)
        detail = (ROOT / 'observer-detail.ps1').read_text()
        codes = set(re.findall(r"return '(lineage-[^']+)'", detail))
        self.assertEqual(codes, schema.LINEAGE_DETAILS)
        lines = native.read_text().splitlines()
        for number, fragment in {7: "throw 'NATIVE_DRIVER_EXITED_OR_IDENTITY_CHANGED'", 73: "throw 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'",
                77: "'parent-row-ambiguous'", 78: "'parent-creation-unavailable'", 88: "'child-row-ambiguous'",
                90: "'child-creation-unavailable'", 92: "'child-older'", 113: 'Require-Live $Driver',
                114: 'Require-Live $Edge', 118: "throw 'NATIVE_APP_OWNERSHIP_AMBIGUOUS'",
                121: "throw 'NATIVE_WEBVIEW_CHILD_NOT_OBSERVED'"}.items():
            self.assertIn(fragment, lines[number - 1])
        self.assertIn('$owned = Get-OwnedApp $drivers[0] $edges[0] $ApplicationPath $snapshot $null $before', observer)
        self.assertNotIn('.Exception', detail); self.assertNotIn('.Message', detail)
        self.assertIn('$Stack.Length -gt 16384', detail)
        self.assertIn('$lines.Count -gt 32', detail)


if __name__ == '__main__':
    unittest.main()
