"""Strict publication/admission of actual Windows engine fixture receipts."""
import copy
import contextlib
import io
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import post_verdict_engine as engine

BIND = {'sourceCommit': 'a'*40, 'sourceTree': 'b'*40, 'controlCommit': 'c'*40}

def fixture():
    rows = []
    for index, case in enumerate(engine.CASES):
        rows.append({'case': case, 'cleanupCount': 1, 'engineVersion': '7.6.9',
            'environmentRestored': True, 'finishCount': 1, 'nativeTestsPassed': 0,
            'nativeTestsRun': 1, 'normalCloseVerified': False, 'passed': True,
            'protocol': 'post-verdict-engine-stop-1', 'state': ('Stopped' if index < 2 else 'Completed' if index == 2 else 'Failed'),
            'stopElapsedMs': 12, 'stopState': 'completed' if index < 2 else 'not-requested'})
    return {'childExitCode': 0, 'childState': 'exited', **BIND, 'engineVersion': '7.6.9',
            'protocol': 'post-verdict-engine-qualification-1', 'results': rows}

class EngineTests(unittest.TestCase):
    def test_closed_types_cases_version_and_bounds(self):
        value = fixture(); self.assertEqual(engine.validate(value, BIND), value)
        changes = [lambda x: x.update(childExitCode=True), lambda x: x.update(engineVersion='7.6.8'),
                   lambda x: x.update(sourceCommit='f'*40), lambda x: x.update(extra='private'),
                   lambda x: x['results'].pop(), lambda x: x['results'].reverse(),
                   lambda x: x['results'][0].update(passed=False), lambda x: x['results'][0].update(stopElapsedMs=3000),
                   lambda x: x['results'][0].update(cleanupCount=True), lambda x: x['results'][0].update(environmentRestored=1),
                   lambda x: x['results'][2].update(stopElapsedMs=15001), lambda x: x['results'][3].update(stopElapsedMs=5001)]
        for change in changes:
            bad = copy.deepcopy(value); change(bad)
            with self.assertRaises(ValueError): engine.validate(bad, BIND)

    def test_actual_publication_receipt_required_and_output_failure_no_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory); path = temp / 'post-verdict-engine-qualified.json'
            path.write_bytes(engine.canonical(fixture()))
            with self.assertRaises(OSError): engine.admit(temp, BIND)
            with patch('builtins.print', side_effect=OSError('closed')):
                with self.assertRaises(OSError): engine.publish(temp, BIND)
            self.assertFalse((temp / 'post-verdict-engine-published.json').exists())
            with contextlib.redirect_stdout(io.StringIO()) as output: engine.publish(temp, BIND)
            self.assertEqual(len(output.getvalue().splitlines()), 4)
            self.assertEqual(engine.admit(temp, BIND), engine.digest(fixture()))
            marker = temp / 'post-verdict-engine-published.json'; saved = marker.read_bytes()
            marker.write_bytes(engine.canonical({**BIND, 'engineQualificationSha256': '0'*64}))
            with self.assertRaises(ValueError): engine.admit(temp, BIND)
            marker.write_bytes(saved)
            path.write_bytes(engine.canonical(fixture()) + b'\n')
            with self.assertRaises(ValueError): engine.admit(temp, BIND)

    def test_links_duplicate_oversize_missing_or_stale_publication_reject(self):
        import os
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory); path = temp / 'post-verdict-engine-qualified.json'
            data = engine.canonical(fixture()); path.write_bytes(data)
            for bad in (b'{"childExitCode":0,' + data[1:], b' '*8193, b'['*1500 + b']'*1500):
                path.write_bytes(bad)
                with self.assertRaises(ValueError): engine.receipt(temp, BIND)
            path.write_bytes(data); os.link(path, temp / 'alias')
            with self.assertRaises(ValueError): engine.receipt(temp, BIND)
            (temp / 'alias').unlink(); path.rename(temp / 'original'); path.symlink_to(temp / 'original')
            with self.assertRaises(ValueError): engine.receipt(temp, BIND)
