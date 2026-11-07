"""Control-only Windows qualification gate; mocks do not claim Windows ABI execution."""
import os
from pathlib import Path
from types import SimpleNamespace
import subprocess
import sys
import unittest
from unittest.mock import patch
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import qualify_wry_pump_windows as qualification

class QualificationTests(unittest.TestCase):
    def test_nonwindows_does_not_launch_fixture(self):
        with patch.object(sys, 'platform', 'darwin'), patch.object(subprocess, 'run', side_effect=AssertionError('NO_LAUNCH')):
            with self.assertRaises(ValueError): qualification.qualify(Path('unused'))

    def test_missing_duplicate_wrong_target_failure_skip_and_zero_rejected(self):
        case = SimpleNamespace(id=lambda: qualification.REQUIRED)
        good = SimpleNamespace(returncode=0, stdout=b'host: x86_64-pc-windows-msvc\n')
        result = SimpleNamespace(wasSuccessful=lambda: True, testsRun=1, requiredStarted=1, requiredPassed=1, skipped=[], expectedFailures=[], unexpectedSuccesses=[])
        with patch.object(sys, 'platform', 'win32'), patch.object(qualification.platform, 'machine', return_value='AMD64'), patch.dict(os.environ, {'WRY_PROBE_RUSTC':'fixture'}), patch.object(subprocess, 'run', return_value=good) as compiler, patch.object(unittest.defaultTestLoader, 'discover', return_value=[case]) as loader, patch.object(unittest, 'TextTestRunner') as runner:
            runner.return_value.run.return_value = result
            value = qualification.qualify(Path('source'))
            self.assertTrue(value['passed']); self.assertEqual(value['nativeLaunches'], 0)
            for selected in ([], [case, case], [SimpleNamespace(id=lambda: 'different')]):
                loader.return_value = selected
                with self.assertRaises(ValueError): qualification.qualify(Path('source'))
            loader.return_value = [case]
            for key, bad in (('requiredStarted',0),('requiredStarted',2),('requiredPassed',0),('requiredPassed',2),('testsRun',0),('skipped',[('x','skip')]),('expectedFailures',[1]),('unexpectedSuccesses',[1]),('wasSuccessful',lambda:False)):
                original = getattr(result,key); setattr(result,key,bad)
                with self.assertRaises(ValueError): qualification.qualify(Path('source'))
                setattr(result,key,original)
            compiler.return_value = SimpleNamespace(returncode=0, stdout=b'host: aarch64-apple-darwin\n')
            with self.assertRaises(ValueError): qualification.qualify(Path('source'))

    def test_workflow_one_native_original_command_and_required_windows_gate(self):
        root=Path(__file__).resolve().parents[2]
        new=(root/'.github/workflows/pump-bracket-once.yml').read_text()
        old=(root/'.github/workflows/wry-receiver-once.yml').read_text()
        self.assertEqual(new.count('id: native'),1)
        old_native=next(line for line in old.splitlines() if '-File "$env:WRY_SOURCE/ui/tauri/native-smoke/supervise.ps1"' in line)
        self.assertEqual(next(line for line in new.splitlines() if '-File "$env:WRY_SOURCE/ui/tauri/native-smoke/supervise.ps1"' in line),old_native)
        self.assertLess(new.index('qualify_wry_pump_windows.py'),new.index('id: native'))
        for required in ('github.run_attempt == 1', "github.event.action == 'opened'", 'github.event.repository.private == false', 'github.event.pull_request.draft == true', 'runs-on: windows-2022', "WRY_PUMP_ENABLED: '1'", 'Remove-Item Env:WRY_PUMP_ENABLED', "$env:WRY_PUMP_ENABLED = '1'", 'persist-credentials: false'):
            self.assertIn(required,new)
        for forbidden in ('upload-artifact','actions/cache','workflow_dispatch','pull_request_target','continue-on-error','secrets.'):
            self.assertNotIn(forbidden,new)
