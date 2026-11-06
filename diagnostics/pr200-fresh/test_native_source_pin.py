"""Offline tamper/trigger contracts only; no hosted or PowerShell execution."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import verify_diagnostic_source as pin


class NativeSourcePinTests(unittest.TestCase):
    def run_case(self, variant='LF', drift=None):
        with tempfile.TemporaryDirectory() as folder:
            control, diagnostic = Path(folder)/'control', Path(folder)/'diagnostic'
            data = b'fixed\nsource\n'
            for root in (control, diagnostic):
                p=root/'diagnostics/pr200-fresh';p.mkdir(parents=True)
                for name in pin.HELPERS: (p/name).write_bytes(data if variant=='LF' else data.replace(b'\n',b'\r\n'))
            if drift=='file': (control/'diagnostics/pr200-fresh'/pin.HELPERS[-1]).write_bytes(b'changed')
            def git(root,*args):
                if args==('status','--porcelain','--untracked-files=all'): return b'?? unexpected' if drift=='dirty' else b''
                if args==('rev-parse','HEAD'): return (('b'*40 if drift=='head' else 'a'*40) if root==control else pin.SOURCE).encode()
                if args==('rev-parse','HEAD^{tree}'): return ('c'*40 if drift=='tree' else pin.TREE).encode()
                if args[0]=='show': return b'changed\n' if drift=='blob' and root==control else data
                self.fail('unexpected git operation')
            with patch.object(pin,'git',side_effect=git):
                return pin.verify(control,diagnostic,'a'*40)

    def test_exact_lf_and_crlf_only(self):
        self.assertTrue(self.run_case()); self.assertTrue(self.run_case('CRLF'))
        for drift in ('file','dirty','head','tree','blob'):
            with self.subTest(drift=drift), self.assertRaises(ValueError): self.run_case(drift=drift)

    def test_missing_or_unknown_expected_head_stops_before_git(self):
        with patch.object(pin,'git') as git:
            for expected in (None,'','HEAD','A'*40):
                with self.assertRaises(ValueError): pin.verify(Path('control'),Path('diagnostic'),expected)
            git.assert_not_called()

    def test_workflow_is_single_ordinary_trial_with_strict_preflight_and_no_transfer(self):
        text=(Path(__file__).resolve().parents[2]/'.github/workflows/pr200-profile-once.yml').read_text()
        self.assertEqual(text.count('phase2.py trial '),1)
        self.assertEqual(text.count('phase2.py preflight '),1)
        self.assertEqual(text.count('cargo +1.99.1 build --release --locked'),1)
        self.assertIn('--features custom-protocol',text)
        self.assertNotIn('startup-diagnostics',text)
        for forbidden in ('upload-artifact','actions/cache','workflow_dispatch','matrix:','secrets.','pull_request_target'):
            self.assertNotIn(forbidden,text)
        for guard in ('github.run_attempt == 1','types: [opened]',"head.ref == 'diagnostic/pr200-profile-once-20261106'", "base.ref == 'diagnostic/pr217-repair-check-20261106'", "base.sha == 'ae7b20c73331aea7e27bb3fb0586aa1d72db13ce'",'github.event.pull_request.draft == true','runs-on: windows-2022','package-manager-cache: false'):
            self.assertIn(guard,text)
        self.assertEqual(text.count('verify_diagnostic_source.py --control'),2)
        self.assertLess(text.index('phase2.py preflight '),text.index('Build locked frontend'))
        self.assertEqual(len(pin.HELPERS),14)


if __name__=='__main__': unittest.main()
