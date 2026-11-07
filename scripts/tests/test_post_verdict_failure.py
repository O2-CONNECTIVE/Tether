"""Actual admission/receipt path, without a native launch."""
import copy
import json
import sys
import tempfile
import hashlib
import os
import unittest
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import post_verdict_failure as subject

class FailureAdmissionTests(unittest.TestCase):
    def setUp(self):
        self.native = json.loads((Path(__file__).parent / 'fixtures/wry240-baseline.json').read_text())['observation']
        self.timing = {'schema': 1, 'purpose': 'diagnostic-hints-only', 'settledMs': 15009, 'outcome': 'timeout', 'samples': [{'stage': 'after-settlement', 'startedMs': 15009, 'completedMs': 15010, 'requestPendingAtStart': False, 'facts': None}]}

    def test_original_pre_header_timeout_is_admitted(self):
        subject.validate_failure(self.native, self.timing)

    def test_non_target_and_typed_negatives(self):
        for field, value in [('schema', True), ('status', 'webview-passed-awaiting-close'), ('phase', 'readiness'), ('droppedRequests', 1), ('errorCode', 'NATIVE_DRIVER_TRANSPORT_FAILED'), ('elapsedMs', True)]:
            with self.subTest(field=field):
                value_native = copy.deepcopy(self.native)
                value_native[field] = value
                with self.assertRaises(ValueError): subject.validate_failure(value_native, self.timing)

    def test_headers_success_extra_and_mismatched_request_rejected(self):
        for mutate in [lambda n: n['requests'][1].update(status=200), lambda n: n['requests'].append(n['requests'][0]), lambda n: n.update(lastRequest=n['requests'][0])]:
            value = copy.deepcopy(self.native)
            mutate(value)
            with self.assertRaises(ValueError): subject.validate_failure(value, self.timing)

    def test_bad_timing_rejected(self):
        for mutate in [lambda t: t.update(outcome='success'), lambda t: t.update(settledMs=True), lambda t: t['samples'][0].update(requestPendingAtStart=True)]:
            value = copy.deepcopy(self.timing)
            mutate(value)
            with self.assertRaises(ValueError): subject.validate_failure(self.native, value)

    def test_real_copy_receipt_binds_exact_bytes(self):
        with tempfile.TemporaryDirectory() as folder:
            directory = Path(folder)
            manifest = {'protocol': 'post-verdict-manifest-1', 'run': '1', 'attempt': 1, 'trial': 1, 'sourceCommit': 'a' * 40, 'sourceTree': 'b' * 40, 'controlCommit': 'c' * 40, 'specimenSha256': 'd' * 64, 'nonce': 'e' * 64}
            raw = subject.canonical(manifest)
            (directory / 'post-verdict.manifest.json').write_bytes(raw)
            native_raw = json.dumps(self.native).encode()
            (directory / 'post-verdict-native.json').write_bytes(native_raw)
            (directory / 'post-verdict-timing.json').write_bytes(json.dumps(self.timing).encode())
            receipt = subject.receipt(directory)
            self.assertEqual(receipt['nativeSha256'], hashlib.sha256(native_raw).hexdigest())
            self.assertEqual(receipt['manifestSha256'], hashlib.sha256(raw).hexdigest())
            self.assertEqual(receipt['verdict'], 'failed')
            self.assertEqual(receipt['nativeTestsPassed'], 0)
            (directory / 'post-verdict.manifest.json').write_bytes(raw + b'\n')
            with self.assertRaises(ValueError): subject.receipt(directory)

    def test_bounded_regular_unique_file_boundary(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'input'
            path.write_bytes(b'{}')
            self.assertEqual(subject.read(path), b'{}')
            with self.assertRaises(ValueError): subject.read(path, 1)
            linked = Path(folder) / 'linked'
            os.link(path, linked)
            with self.assertRaises(ValueError): subject.read(path)
            linked.unlink()
            linked.symlink_to(path)
            with self.assertRaises(ValueError): subject.read(linked)
            path.write_bytes(b'{')
            with self.assertRaises(ValueError): subject.decode(subject.read(path))

    def test_duplicate_keys_rejected(self):
        with self.assertRaises(ValueError): subject.decode(b'{"schema":1,"schema":1}')

if __name__ == '__main__': unittest.main()
