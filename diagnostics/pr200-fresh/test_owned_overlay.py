import copy
from pathlib import Path
import subprocess
import unittest
import owned_overlay as overlay
import owned_control
import owned_schema as schema

ROOT=Path(__file__).resolve().parent


class OwnedOverlayTests(unittest.TestCase):
    def test_exact_lf_crlf_overlay_projects_to_fixed_supervisor(self):
        original=subprocess.check_output(['git','show',overlay.PRODUCT+':ui/tauri/native-smoke/supervise.ps1'],cwd=ROOT)
        for data in (original,original.replace(b'\n',b'\r\n')):
            changed=overlay.overlay_bytes(data);text=changed.decode().replace('\r\n','\n')
            projected=text.replace(overlay.PRELUDE,'').replace(overlay.HOOK,'')
            self.assertEqual(projected,original.decode())
            self.assertEqual(text.count('# BEGIN_OWNED_CAPTURE_HOOK'),1)
            self.assertLess(text.index(overlay.HOOK),text.index("$phase = 'normal-close'"))
            self.assertIn('} $children $app $childHandles $env:NATIVE_PROFILE $OutputDirectory',overlay.HOOK)
            self.assertIn('} catch { }',overlay.HOOK)
            self.assertNotIn('$report',overlay.HOOK)
            self.assertNotIn('.Dispose',overlay.HOOK)
        with self.assertRaises(ValueError):overlay.overlay_bytes(original+overlay.HOOK_SEAM.encode())

    def test_old_trial_request_inspector_and_acceptance_project_exactly(self):
        current=(ROOT/'phase2.py').read_bytes()
        expected=subprocess.check_output(['git','show','613f4ff550cd65334b00b262a0beebbc3e069fad:diagnostics/pr200-fresh/phase2.py'],cwd=ROOT)
        self.assertEqual(owned_control.phase2_projection(current),expected)
        self.assertIn("str(native / 'inspect-driver-trace.ps1')",current.decode())

    def test_fixture_cases_are_closed_and_match_preflight_marker(self):
        import re
        text=(ROOT/'owned-capture.test.ps1').read_text()
        line=next(x for x in text.splitlines() if x.startswith('$knownCases'))
        cases=re.findall("'([^']+)'",line)
        self.assertEqual(len(cases),58);self.assertEqual(len(cases),len(set(cases)))
        self.assertIn('OWNED_CAPTURE_FIXTURE_PASS cases=57',text)
        for name in ('missing-child-name','missing-entry-slot','time-unknown-sticky','set-overrun','borrow-query-throw','borrow-release-fail','hook-save-failure-preserves-close'):
            self.assertIn(name,cases)

    def test_native_source_uses_wait_borrow_and_balanced_directory_close(self):
        cs=(ROOT/'owned-native.cs').read_text();ps=(ROOT/'owned-capture.ps1').read_text()
        self.assertIn('WaitForSingleObject(h,0)',cs);self.assertNotIn('GetExitCodeProcess',cs)
        self.assertIn('Pack=4',cs);self.assertIn('finally { if(!CloseHandles(handles,CloseHandle))',cs)
        self.assertNotIn('OpenProcess',cs);self.assertNotIn('Get-Process',ps)
        self.assertIn('DangerousAddRef',ps);self.assertIn('DangerousRelease',ps);self.assertNotIn('.Dispose(',ps)
        self.assertIn('$Data.overrun = $Data.overrun -or',ps)

    def test_workflow_preserves_one_instrumented_trial_and_no_transfer(self):
        text=(ROOT.parents[1]/'.github/workflows/pr200-owned-hook-once.yml').read_text()
        self.assertEqual(text.count('owned_control.py trial '),1)
        self.assertEqual(text.count('owned_control.py preflight '),1)
        self.assertEqual(text.count('owned_control.py prepare '),1)
        self.assertEqual(text.count('cargo +1.99.1 build --release --locked'),1)
        self.assertIn('ref: '+overlay.PRODUCT,text);self.assertIn('--features custom-protocol',text)
        for guard in ('types: [opened]','github.run_attempt == 1','runs-on: windows-2022','package-manager-cache: false',"base.sha == 'a728caca65ed6e865495e197272c55d1ca6e27e5'"):
            self.assertIn(guard,text)
        for bad in ('upload-artifact','actions/cache','matrix:','workflow_dispatch','secrets.','startup-diagnostics'):
            self.assertNotIn(bad,text)
