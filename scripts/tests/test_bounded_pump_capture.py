"""Offline control regressions. Never starts Host or a native session."""
import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import bounded_pump_capture as gate
import qualify_wry_pump_windows as qualification
import wry_probe_ci as source

ROOT = Path(__file__).resolve().parents[2]
ENV = {'CONTROL_HEAD': 'a' * 40, 'TRIAL_ORDINAL': '1', 'WRY_PIN': gate.SOURCE, 'WRY_TREE': gate.TREE}


class BoundedCaptureTests(unittest.TestCase):
    def setUp(self):
        self.env = patch.dict(os.environ, ENV)
        self.env.start()
        self.addCleanup(self.env.stop)

    def test_qualification_closed_exact_count_types_binding_and_redaction(self):
        good = {**gate.QUALIFICATION, **gate.binding()}
        self.assertEqual(gate.qualification(good), good)
        for key in good:
            bad = good.copy(); del bad[key]
            with self.assertRaises(ValueError): gate.qualification(bad)
        for key, value in [('testsRun', 0), ('testsRun', 40), ('testsRun', 42), ('testsRun', True),
                           ('passed', 1), ('lastErrorCaseExecuted', False), ('nativeLaunches', False),
                           ('trialOrdinal', True), ('sourceCommit', 'b'*40), ('sourceTree', 'c'*40),
                           ('controlCommit', 'd'*40), ('private', 'PRIVATE_SENTINEL')]:
            with self.assertRaises(ValueError): gate.qualification({**good, key: value})
        out = io.StringIO()
        with contextlib.redirect_stdout(out): gate.emit('PUMP_QUALIFICATION', good)
        lines = out.getvalue().splitlines()
        self.assertEqual(lines[2], 'WRY_PUMP_QUALIFICATION_SHA256=' + gate.digest(good))
        self.assertNotIn('PRIVATE_SENTINEL', out.getvalue())

    def test_file_missing_duplicate_size_symlink_hardlink_directory_noncanonical(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory); path = folder/'record'
            with self.assertRaises(OSError): gate.load(path)
            for data in (b'{"a":1,"a":1}', b' '*2049, b'{"a": 1}', b'{}\n', b'\xef\xbb\xbf{}'):
                path.write_bytes(data)
                with self.assertRaises(ValueError): gate.load(path)
            path.write_bytes(b'{}'); self.assertEqual(gate.load(path), {})
            link = folder/'link'; link.symlink_to(path)
            with self.assertRaises(ValueError): gate.load(link)
            link.unlink(); os.link(path, link)
            with self.assertRaises(ValueError): gate.load(path)
            with self.assertRaises(ValueError): gate.load(folder)
            with self.assertRaises(FileExistsError): gate.write_new(path, {})

    def test_actual_execution_tracker_requires_one_required_start_and_success(self):
        class Case(unittest.TestCase):
            def runTest(self): pass
            def id(self): return qualification.REQUIRED
        result = qualification.ExecutedResult(io.StringIO(), True, 0)
        case = Case(); result.startTest(case)
        self.assertEqual(result.requiredStarted, 1); self.assertEqual(result.requiredPassed, 0)
        result.addSuccess(case); self.assertEqual(result.requiredPassed, 1)
        result.startTest(case); result.addSuccess(case)
        self.assertEqual((result.requiredStarted, result.requiredPassed), (2, 2))

    def test_admission_requires_published_exact_qualification_and_every_preflight(self):
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory)
            good = {**gate.QUALIFICATION, **gate.binding()}
            gate.write_new(temp/'qualification.json', good)
            with self.assertRaises(OSError): gate.admit(temp, source)
            proof = {'qualificationSha256': gate.digest(good), **gate.binding()}
            gate.write_new(temp/'qualification-published.json', proof)
            steps = [{'stage': s, 'exitCode': 0} for s in source.STEPS[:12]]
            for bad in [[], steps[:-1], *[steps[:n]+[{**steps[n], 'exitCode':1}]+steps[n+1:] for n in range(12)]]:
                (temp/'steps.json').write_bytes(gate.canonical(bad))
                with self.assertRaises(ValueError): gate.admit(temp, source)
                self.assertFalse((temp/'qualification-admitted.json').exists())
            (temp/'steps.json').write_bytes(gate.canonical(steps))
            gate.admit(temp, source)
            self.assertEqual(gate.load(temp/'qualification-admitted.json'), proof)
            with self.assertRaises(FileExistsError): gate.admit(temp, source)
            (temp/'qualification-published.json').write_bytes(gate.canonical({**proof,'qualificationSha256':'0'*64}))
            with self.assertRaises(ValueError): gate.receipt(temp)

    def body(self):
        return {**gate.binding(), 'allOriginalAndDiagnosticChecksPassed':True,
                'sourceAndSpecimenStable':True, 'deliveryClassificationComplete':True,
                'receiverClassificationComplete':True, 'pumpClassificationComplete':True}

    def test_decision_exact_continuation_stop_timeout_unknown_preflight_and_cap(self):
        good = self.body()
        self.assertEqual([gate.decision(good, True, n) for n in (1,2,3)],
                         ['healthy-1','healthy-2','inconclusive-three-healthy'])
        for n in (1,2,3):
            for passed in (False, None, 1, 'true'):
                self.assertEqual(gate.decision(good, passed, n), 'stop')
            for key in ('allOriginalAndDiagnosticChecksPassed','sourceAndSpecimenStable',
                        'deliveryClassificationComplete','receiverClassificationComplete','pumpClassificationComplete'):
                for invalid in (False, None, 'unknown', 1):
                    self.assertEqual(gate.decision({**good,key:invalid},True,n),'stop')
            # Workflow requires both job success and this exact predecessor token.
            for outcome in ('failure','cancelled','skipped','unknown'):
                self.assertFalse(outcome == 'success' and gate.decision(good,True,n) == 'healthy-'+str(n))
        for bad in (0,4,True,'1'):
            with self.assertRaises(ValueError): gate.decision(good,True,bad)

    def test_finish_keeps_failed_native_record_and_never_emits_continuation(self):
        with tempfile.TemporaryDirectory() as directory:
            temp=Path(directory); out=temp/'output'; out.write_text('')
            body=self.body(); body.pop('trialOrdinal'); body['allOriginalAndDiagnosticChecksPassed']=False
            def publish(*args, **kwargs):
                print('WRY_DIAGNOSTIC_RECORD_BEGIN\n'+gate.canonical(body).decode()+'\nWRY_DIAGNOSTIC_RECORD_SHA256='+gate.digest(body)+'\nWRY_DIAGNOSTIC_RECORD_END')
                return False
            display=io.StringIO()
            with patch.dict(os.environ,{'GITHUB_OUTPUT':str(out)}), contextlib.redirect_stdout(display):
                self.assertEqual(gate.finish(ROOT,temp,SimpleNamespace(publish=publish)),1)
            self.assertIn('WRY_DIAGNOSTIC_RECORD_BEGIN',display.getvalue())
            self.assertIn('"outcome":"stop"',display.getvalue())
            self.assertEqual(out.read_text(),'')

    def test_finish_success_needs_receipts_and_ordinal_three_has_no_token(self):
        for n in (1,2,3):
            with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ,{'TRIAL_ORDINAL':str(n)}):
                temp=Path(directory); out=temp/'output'; out.write_text('')
                body=self.body(); body.pop('trialOrdinal')
                def publish(*args, **kwargs):
                    print('WRY_DIAGNOSTIC_RECORD_BEGIN\n'+gate.canonical(body).decode()+'\nWRY_DIAGNOSTIC_RECORD_SHA256='+gate.digest(body)+'\nWRY_DIAGNOSTIC_RECORD_END')
                    return True
                good={**gate.QUALIFICATION,**gate.binding()}
                proof={'qualificationSha256':gate.digest(good),**gate.binding()}
                gate.write_new(temp/'qualification.json',good)
                gate.write_new(temp/'qualification-published.json',proof)
                gate.write_new(temp/'qualification-admitted.json',proof)
                with patch.dict(os.environ,{'GITHUB_OUTPUT':str(out)}), contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(gate.finish(ROOT,temp,SimpleNamespace(publish=publish)),0)
                self.assertEqual(out.read_text(), 'continuation=healthy-'+str(n)+'\n' if n<3 else '')

    def test_cli_rejection_is_finite_no_arbitrary_exception_or_paths(self):
        display=io.StringIO()
        with patch.object(gate,'binding',side_effect=ValueError('PRIVATE_SENTINEL')), contextlib.redirect_stdout(display):
            self.assertEqual(gate.main(['admit','--root','PRIVATE_SENTINEL','--temp','PRIVATE_SENTINEL']),1)
        self.assertEqual(display.getvalue(),'{"code":"BOUNDED_CAPTURE_REJECTED"}\n')

    def test_real_source_publisher_partial_record_stays_thirteen_sections(self):
        with tempfile.TemporaryDirectory() as directory:
            temp=Path(directory); display=io.StringIO()
            (temp/'raw-python.log').write_text('PRIVATE_SENTINEL')
            with patch.dict(os.environ, {'WRY_DELIVERY_ENABLED':'1','WRY_RECEIVER_ENABLED':'1',
                                         'WRY_PUMP_ENABLED':'1','GITHUB_STEP_SUMMARY':''}), contextlib.redirect_stdout(display):
                self.assertEqual(gate.finish(ROOT,temp,source),1)
            text=display.getvalue(); lines=text.splitlines()
            self.assertNotIn('PRIVATE_SENTINEL',text)
            body=json.loads(lines[1])
            self.assertEqual(len(body['files']),13)
            self.assertFalse(body['allOriginalAndDiagnosticChecksPassed'])
            self.assertEqual(lines[2],'WRY_DIAGNOSTIC_RECORD_SHA256='+gate.digest(body))
            for section in body['files'].values():
                self.assertEqual(section['sha256'],gate.digest(section['data']))

    def test_real_source_summary_failure_preserves_complete_safe_record(self):
        with tempfile.TemporaryDirectory() as directory:
            temp=Path(directory); display=io.StringIO(); out=temp/'output'; out.write_text('')
            summary=temp/'PRIVATE_SENTINEL'; summary.mkdir()
            with patch.dict(os.environ, {'WRY_DELIVERY_ENABLED':'1','WRY_RECEIVER_ENABLED':'1',
                                         'WRY_PUMP_ENABLED':'1','GITHUB_STEP_SUMMARY':str(summary),
                                         'GITHUB_OUTPUT':str(out)}), contextlib.redirect_stdout(display):
                self.assertEqual(gate.main(['finish','--root',str(ROOT),'--temp',str(temp)]),1)
            text=display.getvalue(); lines=text.splitlines()
            self.assertIn('WRY_DIAGNOSTIC_RECORD_BEGIN',text)
            body=json.loads(lines[1])
            self.assertEqual(len(body['files']),13)
            self.assertEqual(lines[2],'WRY_DIAGNOSTIC_RECORD_SHA256='+gate.digest(body))
            self.assertIn('"outcome":"stop"',text)
            self.assertNotIn('PRIVATE_SENTINEL',text)
            self.assertEqual(out.read_text(),'')

    def test_publisher_exception_never_releases_malformed_partial_bytes(self):
        for text in ('PRIVATE_SENTINEL', 'WRY_DIAGNOSTIC_RECORD_BEGIN\nPRIVATE_SENTINEL\n',
                     'WRY_DIAGNOSTIC_RECORD_BEGIN\n{}\nWRY_DIAGNOSTIC_RECORD_SHA256=bad\nWRY_DIAGNOSTIC_RECORD_END\n'):
            def publish(*args, **kwargs):
                print(text,end='')
                raise OSError('PRIVATE_SENTINEL')
            display=io.StringIO()
            with patch.object(gate,'source_module',return_value=SimpleNamespace(publish=publish)), contextlib.redirect_stdout(display):
                self.assertEqual(gate.main(['finish','--root','unused','--temp','unused']),1)
            self.assertEqual(display.getvalue(),'{"code":"BOUNDED_CAPTURE_REJECTED"}\n')

    def test_bad_cli_arguments_never_echo_arbitrary_values(self):
        for args in ([], ['PRIVATE_SENTINEL'], ['admit','--root','PRIVATE_SENTINEL'],
                     ['admit','--root','x','--temp','y','--PRIVATE_SENTINEL']):
            display=io.StringIO()
            with contextlib.redirect_stdout(display), contextlib.redirect_stderr(display):
                self.assertEqual(gate.main(args),1)
            self.assertEqual(display.getvalue(),'{{"code":"BOUNDED_CAPTURE_REJECTED"}}\n'.replace('{{','{').replace('}}','}'))

    def test_workflow_three_exact_serial_original_native_jobs_and_immutable_source(self):
        workflow=(ROOT/'.github/workflows/bounded-pump-capture.yml').read_text()
        original=(ROOT/'.github/workflows/pump-bracket-once.yml').read_text()
        native=next(line for line in original.splitlines() if '-File "$env:WRY_SOURCE/ui/tauri/native-smoke/supervise.ps1"' in line)
        self.assertEqual(workflow.count(native),3)
        self.assertEqual(workflow.count('id: native'),3)
        self.assertEqual(workflow.count('runs-on: windows-2022'),3)
        for required in ('github.run_attempt == 1', "github.event.action == 'opened'",'github.event.repository.private == false',
                         'github.event.pull_request.draft == true','github.event.pull_request.head.repo.full_name == github.repository',
                         'WRY_PIN: '+gate.SOURCE, 'WRY_TREE: '+gate.TREE, '          ref: '+gate.SOURCE):
            self.assertEqual(workflow.count(required),3)
        for n in (2,3):
            self.assertIn(f'needs: trial-{n-1}',workflow)
            self.assertIn(f"needs.trial-{n-1}.result == 'success'",workflow)
            self.assertIn(f"needs.trial-{n-1}.outputs.continuation == 'healthy-{n-1}'",workflow)
        for block in workflow.split('    runs-on: windows-2022')[1:]:
            self.assertLess(block.index(' qualification --root'),block.index('id: native'))
            self.assertLess(block.index(' admit --root'),block.index('Invoke-ProbeStep native'))
            self.assertLess(block.index('-p test_bounded_pump_capture.py'),block.index(' qualification --root'))
            self.assertIn("if ($LASTEXITCODE -ne 0) { throw 'BOUNDED_CAPTURE_GATE_FIXTURE' }",block)
        for forbidden in ('upload-artifact','actions/cache','workflow_dispatch','workflow_call','pull_request_target',
                          'continue-on-error','secrets.','strategy:','matrix:','workflow_run','schedule:'):
            self.assertNotIn(forbidden,workflow)
        # HEAD blobs exist in a depth-one control checkout. Their exact pinned
        # hashes prove unchanged source inputs without depending on any ancestor.
        pinned = {'scripts/wry_probe_ci.py': '87c1a038954e226f4589d1d54bc74caf60f84f05d4fa7adf89d9f8bf615f5a45', 'scripts/wry-probe-step.ps1': '0c1befcf326a16e813a1c80e04061cdcb3648a0499390a6610eb94a6d6ee0adc', 'docs/windows-pump-bracket-probe.md': '8aa70676a7351d925a772772ae8e0fbd4d90e4a2b298a4034c7ae59218cfeaf2'}
        for file, expected in pinned.items():
            blob=subprocess.check_output(['git','-C',str(ROOT),'show','HEAD:'+file])
            self.assertEqual(hashlib.sha256(blob).hexdigest(),expected)
            disk=(ROOT/file).read_bytes()
            self.assertIn(disk,(blob,blob.replace(b'\n',b'\r\n')))
        # Hosted jobs verify the independently checked-out source; no full-history
        # fetch or source object in .control is required.
        source_root=Path(os.environ.get('WRY_SOURCE',str(ROOT)))
        tree=subprocess.check_output(['git','-C',str(source_root),'rev-parse',gate.SOURCE+'^{tree}']).decode().strip()
        self.assertEqual(tree,gate.TREE)
        if 'WRY_SOURCE' in os.environ:
            head=subprocess.check_output(['git','-C',str(source_root),'rev-parse','HEAD']).decode().strip()
            self.assertEqual(head,gate.SOURCE)
