"""Retained-data regressions and explicitly synthetic diagnostic negative cases."""
import copy
import hashlib
import json
from pathlib import Path
import re
import contextlib
import io
import os
import tempfile
import unittest
from unittest.mock import patch

import evidence_schema as schema
import phase2
from test_phase2 import capability, reports, synthetic_classification
from test_profile_contract import valid

ROOT = Path(__file__).resolve().parent


class EvidenceClassificationTests(unittest.TestCase):
    def test_real_retained_record_hashes_and_unknown_causes_remain_unchanged(self):
        record = json.loads((ROOT / 'fixtures/phase2-trial1-record.json').read_text())
        canonical = json.dumps(record, sort_keys=True, separators=(',', ':')).encode()
        self.assertEqual(len(canonical), 26861)
        self.assertEqual(hashlib.sha256(canonical).hexdigest(), 'f634cef6549184533062e33a2f7a9b7fdc4bacc8ac9cdcf6772123416f9169e8')
        original = copy.deepcopy(record)
        for name, value in record['files'].items():
            self.assertEqual(hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest(), record['fileSha256'][name])
        self.assertEqual(schema.retained_classification(record), {
            'capabilityClass': 'not-retained', 'observerSites': ['not-retained']})
        files = record['files']; cap = files['profile-capability.json']; profile = files['profile-observation.json']
        self.assertFalse(cap['capabilityObserved']); self.assertFalse(profile['queryComplete'])
        self.assertEqual(len(profile['rows']), 6)
        self.assertTrue(all(row['mapping'] == 'requested-child-ebwebview' for row in profile['rows']))
        native = {name.removeprefix('observation/'): value for name, value in files.items() if name.startswith('observation/')}
        verdict = phase2.decision([0, 0, 0], native, profile, cap)
        self.assertTrue(verdict['normalCloseVerified']); self.assertTrue(verdict['cleanupConfirmed'])
        self.assertFalse(verdict['continue']); self.assertFalse(verdict['classificationComplete'])
        self.assertFalse(verdict['matchingTimeout']); self.assertTrue(verdict['pr200Blocked'])
        self.assertEqual(record, original)

    def test_new_required_sidecars_cannot_be_silently_omitted(self):
        old_boundary = phase2.decision([0, 0, 0], reports(), valid(), capability())
        self.assertTrue(old_boundary['profileVerified'])
        self.assertFalse(old_boundary['continue'])
        self.assertEqual(old_boundary['stopReason'], 'classification-evidence-incomplete')
        data = synthetic_classification()
        self.assertTrue(phase2.decision([0, 0, 0], reports(), valid(), capability(), data)['continue'])
        for name in data:
            partial = dict(data); del partial[name]
            self.assertFalse(phase2.decision([0, 0, 0], reports(), valid(), capability(), partial)['continue'])

    def test_synthetic_catch_sites_do_not_invent_exception_causes(self):
        data = synthetic_classification(); profile = valid(); profile['queryComplete'] = False
        for site in ['snapshot-query', 'lineage-query', 'process-handle-dispose']:
            data['observer-failure-sites.json'].update(failureCount=1, sites=[site])
            self.assertTrue(schema.classification_complete(data, profile, capability()))
            self.assertFalse(phase2.decision([0, 0, 0], reports(), profile, capability(), data)['continue'])
        data['observer-failure-sites.json']['sites'] = ['unclassified-site']
        self.assertFalse(schema.classification_complete(data, profile, capability()))
        data['observer-failure-sites.json'].update(failureCount=0, sites=[])
        self.assertFalse(schema.classification_complete(data, profile, capability()))

    def test_synthetic_unknown_fields_strings_and_unbounded_counters_reject(self):
        for update in [{'exception': 'PRIVATE_EXCEPTION'}, {'sites': ['PRIVATE_PATH']},
                       {'failureCount': True}, {'failureCount': 4097}, {'sites': ['snapshot-query'] * 17}]:
            data = synthetic_classification()['observer-failure-sites.json']; data.update(update)
            with self.assertRaises(ValueError): schema.observer_sites_safe(data)
        for update in [{'key': 'PRIVATE_CAPABILITY'}, {'flatShape': 'PRIVATE_VALUE'},
                       {'candidateClass': 'PRIVATE_VALUE'}, {'failureSites': ['PRIVATE_EXCEPTION']}, {'requests': 257}]:
            data = synthetic_classification()['profile-capability-shape.json']; data.update(update)
            with self.assertRaises(ValueError): schema.capability_shape_safe(data)

    def test_synthetic_saturation_capture_failure_and_shape_contradiction_never_continue(self):
        for mutate in [lambda d: d['observer-failure-sites.json'].update(failureCount=4096, sites=['snapshot-query'], saturated=True),
                       lambda d: d['profile-capability-shape.json'].update(candidateClass='classification-error'),
                       lambda d: d['profile-capability-shape.json'].update(failureSites=['shape-record-write']),
                       lambda d: d['profile-capability-shape.json'].update(candidateClass='candidate-valid-flat', flatShape='string')]:
            data = synthetic_classification(); mutate(data)
            self.assertFalse(phase2.decision([0, 0, 0], reports(), valid(), capability(), data)['continue'])

    def test_publication_cannot_lose_required_classification_after_continue_was_computed(self):
        from test_phase2 import identity
        import report
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            report.write(root / 'safe/build-identity.json', identity())
            report.write(root / 'safe/trial.json', {'trial': 1, **phase2.decision(
                [0, 0, 0], reports(), valid(), capability(), synthetic_classification())})
            report.write(root / 'safe/profile-observation.json', valid())
            report.write(root / 'safe/profile-capability.json', capability())
            output = io.StringIO()
            with patch.dict(os.environ, GITHUB_STEP_SUMMARY=str(root / 'summary.md')), contextlib.redirect_stdout(output), self.assertRaises(ValueError):
                phase2.publish(root, 1)
            self.assertEqual(output.getvalue(), '')
            self.assertFalse((root / 'summary.md').exists())

    def test_missing_or_malformed_parent_evidence_stays_closed(self):
        for value in [None, [], 'PRIVATE_VALUE']:
            self.assertFalse(schema.classification_complete(synthetic_classification(), value, capability()))
            self.assertFalse(schema.classification_complete(synthetic_classification(), valid(), value))

    def test_static_powershell_site_vocabulary_and_retained_unknown_remain_bounded(self):
        # Static source contract only; does not execute PowerShell/CIM on this Mac.
        text = (ROOT / 'observe-profile.ps1').read_text()
        known_block = text.split('$known = @(', 1)[1].split(')\n', 1)[0]
        self.assertEqual(set(re.findall(r"'([^']+)'", known_block)), schema.OBSERVER_SITES)
        assigned = set(re.findall(r"\$site = '([^']+)'", text))
        self.assertLessEqual(assigned, schema.OBSERVER_SITES)
        self.assertIn('failureCount -lt 4096', text)
        self.assertIn('sites.Count -lt 16', text)
        self.assertNotIn('$_.Exception', text)


if __name__ == '__main__':
    unittest.main()
