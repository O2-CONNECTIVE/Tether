"""Real materialized Wry closures, protocol and Host recorder; SDK is simulated."""
import copy
import json
import os
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

import test_wry_delivery as existing
ROOT = existing.ROOT


class AnchorTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        existing.DeliveryTests.setUpClass()
        cls.base = existing.DeliveryTests
        cls.executables = {}
        for case in ('positive', 'delayed-after-send', 'pre-send-zero', 'entry-before-anchor', 'duplicate', 'early-seal', 'missing-tail-positive'):
            callbacks = list(cls.base.callbacks)
            call = 'assert!(anchor.try_anchor(&"a".repeat(64), &"b".repeat(64)));'
            # Only mutate test copies of actual materialized closure bodies.
            enter = 'let _delivery_completion = webview2_com::rescue_host_delivery::Completion::enter(__delivery_token);'
            sent = 'webview2_com::rescue_host_delivery::Completion::sent(__delivery_token, sent.is_ok());'
            if case in ('positive', 'entry-before-anchor', 'duplicate', 'missing-tail-positive'):
                injection = call
                if case == 'entry-before-anchor': injection = 'let function_already_entered = true;' + call + 'assert!(function_already_entered);'
                if case == 'duplicate': injection += 'assert!(!anchor.try_anchor(&"a".repeat(64), &"b".repeat(64)));'
                callbacks[1] = callbacks[1].replace(enter, injection + enter)
            elif case == 'delayed-after-send': callbacks[1] = callbacks[1].replace(sent, call + sent)
            elif case == 'pre-send-zero': callbacks[1] = callbacks[1].replace(enter, enter + call)
            else: callbacks[1] = callbacks[1].replace(enter, 'let _ = anchor.snapshot(true, true); assert!(!anchor.try_anchor(&"a".repeat(64), &"b".repeat(64)));' + enter)
            if case == 'missing-tail-positive':
                for i, callback in enumerate(callbacks):
                    callbacks[i] = callback.replace('sent\n', 'std::mem::forget(_delivery_completion); sent\n')
            template = (ROOT / 'scripts/tests/fixtures/wry_delivery_main.rs').read_text()
            template = template.replace('"@RECORDER@"', json.dumps((ROOT / 'ui/tauri/src-tauri/src/wry_delivery_probe.rs').as_posix()))
            template = template.replace('let callback: Box', 'let anchor = p.clone(); let callback: Box')
            template = template.replace('@ENV_CALLBACK@', callbacks[0]).replace('@CONTROLLER_CALLBACK@', callbacks[1])
            template = template.replace('print!("{}", p.snapshot(true, true));', 'let original = p.snapshot(true, true); print!("{}", p.anchor_snapshot(&original));')
            source = cls.base.temp / ('anchor-' + case + '.rs'); source.write_text(template)
            exe = source.with_suffix('.exe')
            run = subprocess.run([cls.base.rustc, '--edition=2024', str(source), '--extern', f'webview2_com={cls.base.lib}', '-o', str(exe)], capture_output=True, timeout=30)
            if run.returncode: raise AssertionError(run.stderr.decode())
            cls.executables[case] = exe

    @classmethod
    def tearDownClass(cls): cls.base.tearDownClass()

    def observe(self, case, mode='api'):
        import wry_post_verdict_record as record
        run = subprocess.run([str(self.executables[case]), mode], capture_output=True, timeout=10)
        self.assertEqual(run.returncode, 0, run.stderr.decode())
        path = self.base.temp / 'anchor.json'; path.write_bytes(run.stdout)
        return record.read_record(path, {'appStartupFileClosed': True})

    def test_original_send_after_verdict_and_entry_before_verdict(self):
        import wry_post_verdict_record as record
        for case in ('positive', 'entry-before-anchor', 'missing-tail-positive'):
            value = self.observe(case)
            self.assertTrue(record.send_after_verdict(value))
            self.assertEqual(value['samples'][3]['anchorSeqObserved'], value['anchor']['seq'])
            self.assertEqual(value['delivery']['variant'], 'wry-sdk-delivery-1')

    def test_actual_send_before_anchor_or_zero_capture_never_qualifies(self):
        import wry_post_verdict_record as record
        for case in ('delayed-after-send', 'pre-send-zero', 'duplicate', 'early-seal'):
            self.assertFalse(record.send_after_verdict(self.observe(case)), case)
        self.assertFalse(record.send_after_verdict(self.observe('positive', 'disconnected')))
        # A successful send of inner Err remains a send, not recovered creation.
        self.assertTrue(record.send_after_verdict(self.observe('positive', 'callback-error')))

    def test_closed_reader_and_invalid_order_loss(self):
        import wry_post_verdict_record as record
        import wry_delivery_record as old
        value = self.observe('positive')
        with self.assertRaises(ValueError): old.validate(value)
        for field, replacement in (('schema', True), ('extra', 'secret'), ('variant', 'wry-sdk-delivery-1')):
            bad = copy.deepcopy(value); bad[field] = replacement
            with self.assertRaises(ValueError): record.validate(bad)
        for field, replacement in (('sealed', False), ('inFlight', 1), ('lossDetected', True)):
            bad = copy.deepcopy(value); bad['delivery'][field] = replacement
            self.assertFalse(record.send_after_verdict(bad))
        bad = copy.deepcopy(value); bad['samples'][3]['anchorSeqObserved'] = 0
        self.assertFalse(record.send_after_verdict(bad))
        bad = copy.deepcopy(value); bad['anchor']['elapsedUs'] = value['delivery']['sealElapsedUs'] + 1
        self.assertFalse(record.send_after_verdict(bad))

    def test_real_recorder_deterministic_races(self):
        source = self.base.temp / 'recorder-tests.rs'
        recorder = ROOT / 'ui/tauri/src-tauri/src/wry_delivery_probe.rs'
        source.write_text('#[path = ' + json.dumps(recorder.as_posix()) + '] mod delivery; fn main() {}')
        exe = source.with_suffix('.exe')
        self.base.compile(['--test', str(source), '-o', str(exe)])
        result = subprocess.run([str(exe), '--test-threads=1'], capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertIn(b'test result: ok.', result.stdout)
        self.assertNotIn(b'0 passed;', result.stdout)
        for name in ('zero_captured_before_publication', 'metadata_before_release', 'single_shot_sequence_cas',
                     'seal_during_metadata', 'expiry_or_finish_seal'):
            self.assertIn(name.encode(), result.stdout)

    def test_companion_file_boundary_and_exact_projection(self):
        import wry_post_verdict_record as record
        value = self.observe('positive')
        data = json.dumps(value, separators=(',', ':')).encode()
        path = self.base.temp / 'bounded-companion.json'
        for bad in (data[:-1], data + b'\n', b'{"schema":1,' + data[1:], data + b' ' * record.MAX_BYTES):
            path.write_bytes(bad)
            with self.assertRaises(ValueError): record.read_record(path, {'appStartupFileClosed': True})
        path.write_bytes(data)
        # Actual file grows after metadata was captured. Instrument the real
        # opened stream to require a max+1 read rather than unbounded allocation.
        metadata = path.lstat()
        path.write_bytes(data + b' ' * record.MAX_BYTES)
        actual = path.open('rb')
        owner = self
        class BoundedStream:
            def __enter__(self): return self
            def __exit__(self, *args): actual.close()
            def read(self, count=-1):
                owner.assertEqual(count, record.MAX_BYTES + 1)
                return actual.read(count)
        with patch.object(Path, 'lstat', return_value=metadata), patch.object(Path, 'open', return_value=BoundedStream()):
            with self.assertRaises(ValueError): record.read_record(path, {'appStartupFileClosed': True})
        path.write_bytes(data)
        with self.assertRaises(ValueError): record.read_record(path, {'appStartupFileClosed': False})
        alias = self.base.temp / 'hardlink-companion.json'
        os.link(path, alias)
        try:
            with self.assertRaises(ValueError): record.read_record(path, {'appStartupFileClosed': True})
        finally: alias.unlink()
        alias.symlink_to(path)
        try:
            with self.assertRaises(ValueError): record.read_record(alias, {'appStartupFileClosed': True})
        finally: alias.unlink()
        bad = copy.deepcopy(value); bad['delivery']['samples'][3]['seq'] -= 1
        with self.assertRaises(ValueError): record.validate(bad)
        for field, replacement in (('seq', True), ('anchorSeqObserved', True)):
            bad = copy.deepcopy(value); bad['samples'][3][field] = replacement
            with self.assertRaises(ValueError): record.validate(bad)
