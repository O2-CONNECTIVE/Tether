import copy
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace
import owned_control as control
import owned_schema as schema
import report
from test_owned_schema import complete


def preflight_pass():
    v=control.empty_preflight();v.update(passed=True,casesCompleted=57,failureCase='not-applicable',runtimeVersions={'python':'3.13.17','powershell':'7.6.9'})
    v['stages']={k:{'runtimeExecuted':True,'exitCode':0,'passed':True,'code':'pass'} for k in v['stages']}
    return v


class OwnedControlTests(unittest.TestCase):
    def test_preflight_closed_marker_rejects_unknown_conflicting_and_truncated(self):
        for text,expected in [(b'OWNED_CAPTURE_FIXTURE_FAILED case=pid-reuse\n','pid-reuse'),(b'PRIVATE','unknown'),(b'','missing'),(b'x'*4097,'truncated'),(b'x\nx','conflicting'),(b'OWNED_CAPTURE_FIXTURE_FAILED case=private-key','unknown')]:
            self.assertEqual(control.fixture_failure(text),expected)

    def test_capture_success_never_overwrites_old_red_and_bad_capture_stays_unknown(self):
        for malformed in (False,True):
            with tempfile.TemporaryDirectory() as f:
                root=Path(f);report.write(root/'safe/owned-hook-preflight.json',preflight_pass());report.write(root/'safe/owned-overlay-identity.json',{})
                old={'trial':1,'continue':False,'pr200Blocked':True,'interpretation':'inconclusive','stopReason':'matching-session-timeout'}
                original=json.dumps(old,sort_keys=True).encode()
                calls=[]
                def trial(source,ctrl,target,index,**kwargs):
                    calls.append((index,kwargs));(root/'safe/trial.json').write_bytes(original)
                    capture=complete()
                    if malformed:capture['path']='PRIVATE'
                    report.write(root/'single-trial/output/owned-collection.json',capture)
                    return 1
                with patch.object(control,'verify_source_control'),patch.object(control.overlay,'manifest_safe',return_value={}),patch.object(control.overlay,'verify'),patch.object(control.phase2,'trial',side_effect=trial):
                    self.assertEqual(control.trial(Path('source'),Path('control'),root),1)
                self.assertEqual((root/'safe/trial.json').read_bytes(),original)
                self.assertEqual(len(calls),1);self.assertEqual(calls[0][0],1)
                result=schema.verdict_safe(report.read(root/'safe/owned-capture-verdict.json'))
                self.assertFalse(result['scopePassed']);self.assertTrue(result['originalFailurePreserved'])
                self.assertEqual(result['captureComplete'],not malformed)
                self.assertNotIn('PRIVATE',(root/'safe/owned-collection.json').read_text())

    def test_preflight_each_failure_stops_and_checkpoints_without_retry(self):
        for failure in ('parse','fixture'):
            with tempfile.TemporaryDirectory() as f:
                root=Path(f);report.write(root/'safe/owned-overlay-identity.json',{});calls=[]
                def run(script,marker,private):
                    key='parse' if 'parse-owned' in str(script) else 'fixture';calls.append(key)
                    (private/'stdout.log').write_bytes(b'OWNED_CAPTURE_FIXTURE_FAILED case=slot-mismatch')
                    return {'runtimeExecuted':True,'exitCode':1 if key==failure else 0,'passed':key!=failure,'code':'fixture-rejected' if key==failure else 'pass'}
                with patch.object(control,'verify_source_control'),patch.object(control.overlay,'manifest_safe',return_value={}),patch.object(control.overlay,'verify'),patch.object(control.sys,'version_info',(3,13,17)),patch.dict(os.environ,OWNED_PS_VERSION='7.6.9',OWNED_PS_IDENTITY_MATCH='true'),patch.object(control.powershell_preflight,'run_stage',side_effect=run):
                    self.assertEqual(control.preflight(Path('source'),Path('control'),root),1)
                value=schema.preflight_safe(report.read(root/'safe/owned-hook-preflight.json'))
                self.assertFalse(value['passed']);self.assertEqual(calls,['parse'] if failure=='parse' else ['parse','fixture'])
                self.assertEqual(value['casesCompleted'],0)

    def test_preflight_wrapper_uncertainty_is_saved_without_reexecution(self):
        with tempfile.TemporaryDirectory() as f:
            root=Path(f);report.write(root/'safe/owned-overlay-identity.json',{});calls=[]
            def run(*args):calls.append(1);raise OSError('PRIVATE')
            with patch.object(control,'verify_source_control'),patch.object(control.overlay,'manifest_safe',return_value={}),patch.object(control.overlay,'verify'),patch.object(control.sys,'version_info',(3,13,17)),patch.dict(os.environ,OWNED_PS_VERSION='7.6.9',OWNED_PS_IDENTITY_MATCH='true'),patch.object(control.powershell_preflight,'run_stage',side_effect=run):
                with self.assertRaises(OSError):control.preflight(Path('source'),Path('control'),root)
            v=schema.preflight_safe(report.read(root/'safe/owned-hook-preflight.json'))
            self.assertEqual(len(calls),1);self.assertEqual(v['stages']['parse']['code'],'wrapper-uncertain');self.assertIsNone(v['stages']['parse']['runtimeExecuted'])
