"""Closed pump summary and publication boundary, not a Windows timeout repro."""
import contextlib
import io
import json
import os
from pathlib import Path
import sys
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
import wry_pump_record as record
import wry_probe_ci as ci


def specimen():
    return dict(schema=1, variant='wry-pump-brackets-1', purpose='diagnostic-hints-only',
                registered=True, sealRequested=True, sealed=True, lossDetected=False,
                slots=[dict(slot=name, relation='original-wait', state='get-message-bracket',
                            **{key: int(key in ('scopeEntriesCapped', 'waitEntriesCapped', 'getEntriesCapped'))
                               for key in record.COUNTS}) for name in ('environment', 'controller')])


class PumpRecordTests(unittest.TestCase):
    def test_valid_known_and_unknown_records(self):
        value = specimen()
        self.assertTrue(record.complete(value))
        value['slots'][0].update(state='unknown', relation='unknown')
        self.assertFalse(record.complete(value))
        value['lossDetected'] = True
        value['slots'][1].update(state='unknown', relation='unknown')
        record.validate(value)

    def test_closed_fields_types_and_privacy(self):
        mutations = [lambda v: v.update(private='PRIVATE_SENTINEL'),
                     lambda v: v.pop('sealRequested'),
                     lambda v: v.update(schema=True),
                     lambda v: v.update(registered=1),
                     lambda v: v['slots'][0].update(state='PRIVATE_SENTINEL'),
                     lambda v: v['slots'][0].update(relation='same-creation'),
                     lambda v: v['slots'][0].update(path='PRIVATE_SENTINEL'),
                     lambda v: v['slots'][0].pop('getEntriesCapped'),
                     lambda v: v['slots'][0].update(getEntriesCapped=True),
                     lambda v: v['slots'][0].update(getEntriesCapped=3),
                     lambda v: v['slots'].reverse(),
                     lambda v: v.update(lossDetected=True),
                     lambda v: v.update(registered=False),
                     lambda v: v.update(sealRequested=False),
                     lambda v: v.update(sealed=False),
                     lambda v: v['slots'][0].update(state='missing'),
                     lambda v: v['slots'][0].update(waitEntriesCapped=0)]
        for mutate in mutations:
            value = specimen(); mutate(value)
            with self.assertRaises(ValueError): record.validate(value)

    def test_mode_requires_both_predecessor_sections(self):
        for delivery, receiver, pump in (('', '', '1'), ('1', '', '1'), ('', '1', '1'), ('1', '1', 'PRIVATE_SENTINEL')):
            env = dict(os.environ, WRY_DELIVERY_ENABLED=delivery, WRY_RECEIVER_ENABLED=receiver, WRY_PUMP_ENABLED=pump)
            result = subprocess.run([sys.executable, '-B', str(ROOT/'scripts/wry_probe_ci.py'),
                                     'publish', '--root', 'PRIVATE_SENTINEL', '--temp', 'PRIVATE_SENTINEL'],
                                    env=env, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stderr, b'')
            self.assertEqual(result.stdout.strip(), b'{"code":"WRY_DIAGNOSTIC_RECORD_REJECTED"}')

    def test_file_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'record.json'
            text = json.dumps(specimen(), separators=(',', ':'))
            path.write_text(text)
            self.assertTrue(record.complete(record.read_record(path, {'appStartupFileClosed': True})))
            for collection in (None, [], {}, {'appStartupFileClosed': False}):
                with self.assertRaises(ValueError): record.read_record(path, collection)
            for invalid in (text+'\n', text.replace('"schema":1', '"schema":1,"schema":1'),
                            '['*2000+']'*2000, '{"private":"PRIVATE_SENTINEL"}'):
                path.write_text(invalid)
                with self.assertRaises(ValueError): record.read_record(path, {'appStartupFileClosed': True})
            path.write_text(text)
            alias = Path(directory) / 'alias'; alias.symlink_to(path)
            with self.assertRaises(ValueError): record.read_record(alias, {'appStartupFileClosed': True})

    def test_publisher_preserves_native_and_rejects_missing_diagnostic(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root/'tools/app-startup').mkdir(parents=True)
            path = root/'tools/app-startup/wry-pump-brackets.json'
            native = {'nativeTestsRun': 1, 'nativeTestsPassed': 0, 'appStartupFileClosed': True}
            def load(path, *args):
                if path.name == 'native-result.json': return native
                raise ValueError()
            with patch.dict(os.environ, {'WRY_RECEIVER_ENABLED':'1', 'WRY_PUMP_ENABLED':'1', 'CONTROL_HEAD':'a'*40}), \
                 patch.object(ci, 'load', side_effect=load), patch.object(ci, 'native_summary', side_effect=lambda v:v), \
                 patch.object(ci, 'git', return_value=b'b'*40):
                for valid in (True, False, None):
                    path.write_text(json.dumps(specimen(), separators=(',', ':')) if valid else '{"private":"PRIVATE_SENTINEL"}')
                    if valid is None: path.unlink()
                    output = io.StringIO()
                    with contextlib.redirect_stdout(output): self.assertFalse(ci.publish(root, root, True))
                    text = output.getvalue(); body = json.loads(text.splitlines()[1])
                    self.assertEqual(body['files']['native']['data'], native)
                    self.assertEqual(body['pumpClassificationComplete'], bool(valid))
                    self.assertFalse(body['allOriginalAndDiagnosticChecksPassed'])
                    self.assertNotIn('PRIVATE_SENTINEL', text)
                    if not valid: self.assertEqual(body['files']['pump']['data'], {'state':'unavailable-or-rejected'})
