"""Real retained PR220 replay plus explicitly synthetic strict-gate controls."""
import copy
import hashlib
import json
from pathlib import Path
import unittest
import phase2
import report

ROOT=Path(__file__).resolve().parent


def retained():
    return json.loads((ROOT/'fixtures/pr220-trial1-record.json').read_text())


def decide(files):
    return phase2.decision([0,0,0], {name:files['observation/'+name] for name in report.FILES if 'observation/'+name in files},
        files['profile-observation.json'],files['profile-capability.json'],
        {name:files[name] for name in ('profile-capability-shape.json','observer-failure-sites.json')})


class ProfileEvidenceContractTests(unittest.TestCase):
    def test_real_record_replay_never_promotes_classified_failure_to_profile_success(self):
        record=retained();canonical=lambda x:json.dumps(x,sort_keys=True,separators=(',',':')).encode()
        self.assertEqual(len(canonical(record)),28316)
        self.assertEqual(hashlib.sha256(canonical(record)).hexdigest(),'544150f45a78f72559a02d9ccec6b5b8014fdfcf38a64d22bdf9c80cca81797d')
        for name,value in record['files'].items():self.assertEqual(hashlib.sha256(canonical(value)).hexdigest(),record['fileSha256'][name])
        files=record['files'];result=decide(files)
        self.assertTrue(result['classificationComplete']);self.assertTrue(result['cleanupConfirmed'])
        self.assertFalse(result['profileVerified']);self.assertFalse(result['continue']);self.assertFalse(result['matchingTimeout'])
        self.assertEqual(files['profile-capability-shape.json']['pathPredicate'],'drive-prefix')
        self.assertEqual(files['observer-failure-sites.json']['failureCount'],6)
        # The saved wrapper overwrites this decision reason for observer exit1.
        self.assertEqual(result['stopReason'],'profile-or-required-evidence-unconfirmed')
        self.assertEqual(files['trial.json']['stopReason'],'evidence-observer-or-byte-integrity-failure')

    def test_independent_rows_or_one_repaired_axis_never_prove_native_correspondence(self):
        for axis in ('observer','capability','native'):
            files=copy.deepcopy(retained()['files'])
            if axis=='observer':
                files['profile-observation.json'].update(queryComplete=True,allObservedWebviewsCovered=True)
                for row in files['profile-observation.json']['rows']:row['mapping']='requested-root'
            if axis=='capability':files['profile-capability.json'].update(capabilityObserved=True,capabilityMatchesRequested=True,capabilityMapping='requested-root')
            if axis=='native':
                for row in files['observation/native-result.json']['childCloseFacts']['rows']:
                    if row['role']=='webview':row['profileMatch']=True
            self.assertFalse(decide(files)['continue'])

    def test_synthetic_all_strict_gates_positive_and_single_gate_negative_controls(self):
        files=copy.deepcopy(retained()['files'])
        files['profile-observation.json'].update(queryComplete=True,allObservedWebviewsCovered=True)
        for row in files['profile-observation.json']['rows']:row['mapping']='requested-root'
        files['profile-capability.json'].update(capabilityObserved=True,capabilityMatchesRequested=True,capabilityMapping='requested-root')
        files['profile-capability-shape.json'].update(candidateClass='candidate-valid-nested',pathPredicate='accepted')
        files['observer-failure-sites.json'].update(failureCount=0,sites=[],details=[])
        for row in files['observation/native-result.json']['childCloseFacts']['rows']:
            if row['role']=='webview':row['profileMatch']=True
        self.assertTrue(decide(files)['continue'])
        for mutate in (
            lambda x:x['profile-observation.json'].update(queryComplete=False),
            lambda x:x['profile-observation.json']['rows'][0].update(mapping='requested-child-ebwebview'),
            lambda x:x['profile-capability.json'].update(capabilityObserved=False),
            lambda x:x['observation/native-result.json']['childCloseFacts']['rows'][0].update(profileMatch=False),
            lambda x:x['observation/cleanup-audit.json'].update(queryComplete=False)):
            changed=copy.deepcopy(files);mutate(changed);self.assertFalse(decide(changed)['continue'])

    def test_poll_fixture_exists_at_real_source_seam_without_loading_os_code(self):
        text=(ROOT/'observer-poll.test.ps1').read_text()
        self.assertIn("Load-Functions $observer @('New-ObserverFailures', 'Add-ObserverFailure')",text)
        self.assertIn("Load-Functions $processes @('Get-OwnedApp', 'Require-Live', 'Same-Process', 'Test-LineageCreation')",text)
        self.assertIn("$poll = [scriptblock]::Create($text.Substring($start, $end - $start))",text)
        self.assertIn('. $poll',text)
        self.assertNotIn('Get-CimInstance',text);self.assertNotIn('Add-Type',text)
        self.assertIn("@('zero', 'valid')",text);self.assertIn("@('valid', 'zero')",text)
