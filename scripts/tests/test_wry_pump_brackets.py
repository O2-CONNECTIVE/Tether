"""Actual pinned pump/protocol/recorder seam; fake Get/Dispatch are not a Windows repro."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
import prepare_wry_delivery as prep


class PumpBracketTests(unittest.TestCase):
    def test_exact_pump_held_brackets_returns_and_negative_controls(self):
        archive = Path(os.environ['WRY_COMPLETION_CRATE'])
        rustc = os.environ['WRY_PROBE_RUSTC']
        original = prep.com_files(archive)['src/lib.rs'].decode()
        patched = prep.patch_com(original)
        self.assertEqual(prep.patch_com(patched, True), original)
        self.assertEqual(hashlib.sha256(patched.encode()).hexdigest(), prep.COM_POST)
        self.assertEqual(patched.count('rx.try_recv()'), 1)
        fixture = ROOT / 'scripts/tests/fixtures/wry_pump_brackets.rs'
        with tempfile.TemporaryDirectory(prefix='pump-brackets-') as directory:
            root = Path(directory)
            (root / 'main.rs').write_bytes(fixture.read_bytes())
            (root / 'protocol.rs').write_bytes(prep.protocol_bytes())
            recorder = ROOT / 'ui/tauri/src-tauri/src/wry_delivery_probe.rs'
            (root / 'recorder_path.rs').write_text(f'#[path={json.dumps(str(recorder))}] mod recorder;\n')
            exe = root / ('run.exe' if os.name == 'nt' else 'run')

            def build(source):
                (root / 'pump.rs').write_text(source[source.index('pub fn wait_with_pump'):])
                result = subprocess.run([rustc, '--edition=2024', '--cfg', 'rescue_host_pump_fixture',
                                         str(root / 'main.rs'), '-o', str(exe)],
                                        capture_output=True, timeout=40)
                self.assertEqual(result.returncode, 0, result.stderr.decode(errors='replace'))

            def run(case, poll_case=""):
                result = subprocess.run([str(exe), case, poll_case], capture_output=True, timeout=15)
                self.assertEqual(result.returncode, 0, result.stderr.decode(errors='replace'))
                value = json.loads(result.stdout)
                from wry_pump_record import validate
                return validate(value)

            build(patched)
            expected = {
                # state, get entries/error/quit/other returns, dispatch entries/returns
                'get-held': ('get-message-bracket', 1, 0, 0, 0, 0, 0),
                'dispatch-held': ('dispatch-message-bracket', 1, 0, 0, 1, 1, 0),
                'caps': ('get-message-bracket', 2, 0, 0, 2, 2, 2),
                'ready': ('wait-ended', 0, 0, 0, 0, 0, 0),
                'disconnected': ('wait-ended', 1, 0, 1, 0, 0, 0),
                'error': ('wait-ended', 1, 1, 0, 0, 0, 0),
                'quit': ('wait-ended', 1, 0, 1, 0, 0, 0),
                'positive': ('wait-ended', 1, 0, 0, 1, 1, 1),
                'other-negative': ('wait-ended', 1, 0, 0, 1, 1, 1),
            }
            fields = ('state', 'getEntriesCapped', 'getErrorReturnsCapped',
                      'getQuitReturnsCapped', 'getOtherReturnsCapped',
                      'dispatchEntriesCapped', 'dispatchReturnsCapped')
            for case, values in expected.items():
                with self.subTest(case=case, windows_last_error=os.name == 'nt'):
                    value = run(case)
                    self.assertTrue(value['registered'] and value['sealRequested'] and value['sealed'])
                    self.assertFalse(value['lossDetected'])
                    row = value['slots'][0]
                    self.assertEqual(row['relation'], 'original-wait')
                    self.assertEqual(tuple(row[k] for k in fields), values)
                    self.assertEqual(row['waitEntriesCapped'], 1)
                    self.assertEqual(row['waitDropsCapped'], int(case not in ('get-held', 'dispatch-held', 'caps')))
                    self.assertEqual(value['slots'][1]['relation'], 'unknown')
                    self.assertEqual(value['slots'][1]['state'], 'missing')
            # A completed unbound ready-only poll has no lifecycle/bracket hook.
            # It must poison an open target window, even after old delivery seal.
            for poll_case in ('foreign-ready', 'foreign-after-old-seal'):
                value = run('get-held', poll_case)
                self.assertTrue(value['lossDetected'])
                self.assertTrue(all(row['relation'] == 'unknown' for row in value['slots']))
            outside_baseline = run('ready')
            for poll_case in ('outside-ready', 'outside-quit'):
                value = run('ready', poll_case)
                self.assertFalse(value['lossDetected'])
                self.assertEqual(value['slots'][0]['state'], 'wait-ended')
                self.assertEqual(value['slots'][0]['relation'], 'original-wait')
                self.assertEqual(value, outside_baseline, 'outside wait changed target counts/state')
            # Mutants are applied to actual patched pump bytes, not a modeled FSM.
            for seam, case in (
                ('rescue_host_delivery::Pump::phase(2);', 'get-held'),
                ('rescue_host_delivery::Pump::phase(3);', 'dispatch-held'),
                ('rescue_host_delivery::Pump::dispatch_return();', 'positive'),
            ):
                self.assertEqual(patched.count(seam), 1)
                for replacement in ('', seam + '\n' + seam):
                    mutant = patched.replace(seam, replacement, 1)
                    self.assertNotEqual(mutant, patched)
                    try:
                        restored = prep.patch_com(mutant, True)
                    except ValueError:
                        pass
                    else:
                        self.assertNotEqual(restored, original, "mutant restored trusted original bytes")
                    build(mutant)
                    value = run(case)
                    self.assertNotEqual(value['slots'][0]['state'], expected[case][0],
                                        'missing/duplicate bracket escaped the public seam assertion')
            # Scalar identity adapter omission must no longer inverse-project.
            seam = 'rescue_host_delivery::Pump::get_return(WindowsAndMessaging::GetMessageA(&mut msg, None, 0, 0).0)'
            self.assertEqual(patched.count(seam), 1)
            mutant = patched.replace(seam, 'WindowsAndMessaging::GetMessageA(&mut msg, None, 0, 0).0', 1)
            with self.assertRaises(ValueError):
                prep.patch_com(mutant, True)
            build(mutant)
            value = run('quit')
            self.assertNotEqual(value['slots'][0]['state'], 'wait-ended')

            if os.name == 'nt':
                # On Windows only: removing the actual adapter guard must expose
                # the deliberate pre-TLS clobber to the original error conversion.
                protocol = prep.protocol_bytes().decode()
                seam = 'pub(crate) fn get_return(value: i32) -> i32 {\n        let _last_error = last_error::Saved::save();'
                self.assertEqual(protocol.count(seam), 1)
                (root / 'protocol.rs').write_text(protocol.replace(seam, 'pub(crate) fn get_return(value: i32) -> i32 {', 1))
                build(patched)
                result = subprocess.run([str(exe), 'error'], capture_output=True, timeout=15)
                self.assertNotEqual(result.returncode, 0, 'LastError guard mutation was not detected')
