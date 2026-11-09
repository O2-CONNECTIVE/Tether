"""Validate the workflow success gate with synthetic files, never native work."""
import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import textwrap
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent


def project(adapter='cli', changes=None):
    source = (ROOT / '.github/workflows/producer-connection.yml').read_text()
    source = textwrap.dedent(source.split("<<'PY'\n", 1)[1].split('\n          PY', 1)[0])
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        name = 'TestCIActualProducerConnection'
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentpipe'
        record = dict(outcome='passed', cleanup='confirmed', test=name, invocation='1' * 32,
                      helper_sha256='2' * 64, fixture_retained=False,
                      work_cli_sha256='4' * 64, work_cli_session=adapter == 'cli',
                      work_cli_dictionary=adapter == 'cli')
        record.update(changes or {})
        report = root / 'result.json'
        report.write_text(json.dumps(record))
        joined = dict(outcome='passed', cleanup='confirmed', test=name,
                      invocation='1' * 32, original_test_exit=0)
        report.with_suffix('.supervisor.json').write_text(json.dumps(joined))
        report.with_suffix('.verdict.json').write_text(json.dumps(joined))
        events = [{'Package': package, 'Test': name, 'Action': 'run'},
                  {'Package': package, 'Test': name, 'Action': 'pass'},
                  {'Package': package, 'Action': 'pass'}]
        (root / 'connection.jsonl').write_text('\n'.join(map(json.dumps, events)))
        env = dict(LANE='normal', CI_PRODUCER_ADAPTER=adapter, CI_PRODUCER_SHA256='2' * 64,
                   CI_PRODUCER_SOURCE_SHA='3' * 40, CI_PRODUCER_INVOCATION='1' * 32,
                   CI_WORK_SHA256='4' * 64, CI_PRODUCER_RUN_DIR=str(root),
                   CI_PRODUCER_RESULT=str(report), GITHUB_STEP_SUMMARY=str(root / 'summary'))
        output = io.StringIO()
        with patch.dict(os.environ, env, clear=True), patch('sys.argv', ['-', 'connection', package, name]), contextlib.redirect_stdout(output):
            exec(compile(source, '<workflow success gate>', 'exec'), {})
        return json.loads(output.getvalue())


class DictionaryEvidenceTests(unittest.TestCase):
    def test_cli_requires_dictionary_success_not_legacy_session_marker(self):
        public = project()
        self.assertEqual(public['work_cli_dictionary'], 'list-get_execution-read_output')
        self.assertNotIn('invocation', public)
        for value in (False, None, 0, 1, 'true'):
            with self.subTest(marker=value), self.assertRaisesRegex(SystemExit, 'NOT_VERIFIED'):
                project(changes={'work_cli_dictionary': value})

    def test_json_lane_never_claims_dictionary_execution(self):
        public = project(adapter='json')
        self.assertNotIn('work_cli_dictionary', public)
        for value in (True, None, 0, 'false'):
            with self.subTest(marker=value), self.assertRaisesRegex(SystemExit, 'NOT_VERIFIED'):
                project(adapter='json', changes={'work_cli_dictionary': value})

    def test_dictionary_marker_does_not_replace_original_cleanup_and_session(self):
        for change in ({'cleanup': 'unconfirmed'}, {'outcome': 'failed-unknown'},
                       {'work_cli_session': False}, {'work_cli_sha256': '5' * 64},
                       {'fixture_retained': True}):
            with self.subTest(change=change), self.assertRaisesRegex(SystemExit, 'NOT_VERIFIED'):
                project(changes=change)


if __name__ == '__main__':
    unittest.main()
