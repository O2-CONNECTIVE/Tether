"""Synthetic recorder tests; never invoke PowerShell or any native function."""
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import poll_fixture_only as recorder


def stage(code='pass'):
    return {'runtimeExecuted':code not in {'not-run','command-unavailable'},'exitCode':0 if code=='pass' else 1 if code=='fixture-rejected' else None,'passed':code=='pass','code':code}


def chain(root, failure=None, code='fixture-rejected', raw=b'PROFILE_OBSERVER_POLL_FIXTURE_FAILED\n'):
    calls=[]
    def run(script,marker,private):
        index=len(calls);calls.append(Path(script).name)
        if index==failure:
            (private/'stdout.log').write_bytes(raw)
            return stage(code)
        return stage()
    value=recorder.run_chain(SimpleNamespace(run_stage=run),root/'source',root/'parser.ps1',root)
    return value,calls


class PollFixtureOnlyTests(unittest.TestCase):
    def test_exact_two_stage_pass_reaches_six_cases(self):
        with tempfile.TemporaryDirectory() as f:
            value,calls=chain(Path(f))
            self.assertEqual(calls,['parser.ps1','observer-poll.test.ps1']);self.assertTrue(value['passed'])
            self.assertEqual(value['parseFilesVerified'],4);self.assertEqual(value['casesCompleted'],6)
            self.assertEqual(value['failure'],'not-applicable')

    def test_each_failed_stage_stops_without_retry_or_native(self):
        for index in range(2):
            for code in ('fixture-rejected','command-timeout','command-unavailable'):
                with tempfile.TemporaryDirectory() as f:
                    value,calls=chain(Path(f),index,code)
                    self.assertEqual(len(calls),index+1);self.assertFalse(value['passed']);self.assertEqual(value['casesCompleted'],0)
                    if index==0:self.assertEqual(value['stages']['poll']['code'],'not-run')
                    self.assertEqual(value['failure'],'known' if index==1 and code=='fixture-rejected' else 'not-observed')

    def test_fixed_failure_marker_unknown_missing_conflict_and_truncation(self):
        marker=b'PROFILE_OBSERVER_POLL_FIXTURE_FAILED\n'
        for raw,exit,status in [(marker,1,'known'),(marker,2,'unknown'),(b'',1,'missing'),(b'PRIVATE_TEXT',1,'unknown'),(marker*2,1,'conflicting'),(marker+recorder.MARKERS[1].encode(),1,'conflicting'),(b'x'*4097,1,'truncated'),(b'\xff',1,'unknown')]:
            self.assertEqual(recorder.failure_marker(raw,exit),status)

    def test_malformed_unknown_or_inconsistent_stage_evidence_never_passes(self):
        with tempfile.TemporaryDirectory() as f:value,_=chain(Path(f))
        for mutate in (lambda x:x.update(path='PRIVATE_PATH'),lambda x:x.update(casesCompleted=True),lambda x:x.update(casesCompleted=0),lambda x:x['stages']['poll'].update(exitCode=1),lambda x:x.update(failure='known'),lambda x:x['stages']['parse'].update(runtimeExecuted=False)):
            changed=copy.deepcopy(value);mutate(changed)
            with self.assertRaises(ValueError):recorder.chain_safe(changed)

    def test_complete_publication_hashes_and_no_arbitrary_output(self):
        with tempfile.TemporaryDirectory() as f:
            root=Path(f);data,_=chain(root)
            identity={'schema':1,'controlCommit':'a'*40,'controlTree':'b'*40,'fixtureSourceCommit':recorder.SOURCE,'fixtureSourceTree':recorder.TREE,'reviewedPatchSha256':recorder.PATCH,'sourceVerified':True,'sourceStableAfter':True,'sourceFileSha256':{k:'c'*64 for k in recorder.FILES},'controlParserSha256':'d'*64,'runnerLabel':'windows-2022','pythonVersion':'3.13.17','powershellVersion':'7.6.9'}
            out=io.StringIO()
            with patch.dict(os.environ,GITHUB_STEP_SUMMARY=str(root/'summary.md')),contextlib.redirect_stdout(out):recorder.publish(identity,data,'fixture-passed',recorder.chain_safe)
            lines=out.getvalue().splitlines();record=json.loads('\n'.join(lines[1:-1]));canonical=lambda x:json.dumps(x,sort_keys=True,separators=(',',':')).encode()
            self.assertEqual(lines[0],'BEGIN_PR220_POLL_FIXTURE_RECORD sha256:'+recorder.sha(canonical(record)))
            self.assertEqual(set(record['files']),{'identity.json','poll-preflight.json'})
            for name,item in record['files'].items():self.assertEqual(record['fileSha256'][name],recorder.sha(canonical(item)))
            self.assertNotIn(str(root),out.getvalue());self.assertLess(len(out.getvalue()),60000)
            changed={**identity,'path':'PRIVATE_PATH'};out=io.StringIO()
            with contextlib.redirect_stdout(out),self.assertRaises(ValueError):recorder.publish(changed,data,'fixture-passed',recorder.chain_safe)
            self.assertEqual(out.getvalue(),'')

    def test_observe_fixed_chain_once_and_source_drift_or_runtime_boundary_stops(self):
        for drift, platform, expected_outcome, expected_calls in ((False,'win32','fixture-passed',2),(True,'win32','recorder-rejected',2),(False,'linux','boundary-rejected',0)):
            with tempfile.TemporaryDirectory() as f:
                root=Path(f);control=root/'control';source=root/'fixture-source'
                parser=control/'diagnostics/pr200-fresh/parse-poll-sources.ps1';parser.parent.mkdir(parents=True);parser.write_bytes(b'fixed parser\n')
                calls=[]
                def run(script,marker,private):calls.append(script);return stage()
                module=SimpleNamespace(run_stage=run)
                def git(path,*args):
                    if args==('rev-parse','HEAD'):return ('a'*40).encode()
                    if args==('rev-parse','HEAD^{tree}'):return ('b'*40).encode()
                    if args[0]=='show':return parser.read_bytes()
                    return b''
                before={key:'c'*64 for key in recorder.FILES};after={key:'d'*64 for key in before} if drift else before
                env={'RUNNER_ENVIRONMENT':'github-hosted','RUNNER_OS':'Windows','RUNNER_ARCH':'X64','EXPECTED_CONTROL':'a'*40,'RECORDER_PS_PATH_MATCH':'true','RECORDER_POWERSHELL_VERSION':'7.6.9','GITHUB_STEP_SUMMARY':str(root/'summary.md')}
                output=io.StringIO()
                with patch.object(recorder.sys,'platform',platform),patch.object(recorder.sys,'version_info',(3,13,17)),patch.dict(os.environ,env),patch.object(recorder,'git',side_effect=git),patch.object(recorder,'source_hashes',side_effect=[before,after]),patch.object(recorder,'load_preflight',return_value=module),contextlib.redirect_stdout(output):
                    exit_code=recorder.observe(source,control,root/'observation')
                record=json.loads('\n'.join(output.getvalue().splitlines()[1:-1]));self.assertEqual(record['outcome'],expected_outcome)
                self.assertEqual(len(calls),expected_calls);self.assertEqual(exit_code,0 if expected_outcome=='fixture-passed' else 1)
                self.assertNotIn(str(root),output.getvalue())

    def test_static_workflow_scope_and_source_pin(self):
        root=Path(__file__).resolve().parents[2];text=(root/'.github/workflows/pr220-poll-fixture-once.yml').read_text()
        self.assertEqual(text.count('      - name:'),4);self.assertEqual(text.count('poll_fixture_only.py --source'),1)
        self.assertIn('ref: '+recorder.SOURCE,text);self.assertIn('runs-on: windows-2022',text)
        for guard in ('types: [opened]','github.run_attempt == 1',"head.ref == 'diagnostic/pr220-poll-fixture-once-20261106'", "base.ref == 'diagnostic/pr200-profile-once-20261106'", "base.sha == 'e9498fdf4dee714e472ea11118eb477853b3efe5'",'github.event.pull_request.draft == true','contents: read'):
            self.assertIn(guard,text)
        for forbidden in ('phase2.py','setup-node','rustup','cargo','npm ','upload-artifact','actions/cache','workflow_dispatch','matrix:','secrets.'):
            self.assertNotIn(forbidden,text)
