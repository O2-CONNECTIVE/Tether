"""Exercise the public workflow projection with synthetic markers, never owners."""
import contextlib
import io
import json
import os
from pathlib import Path
import re
import tempfile
import textwrap
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
FIELDS = {
    'producer_request_intent': ['unavailable', 'on', 'off'],
    'producer_receipt': ['unavailable', 'missing', 'accepted', 'rejected', 'stale', 'unknown', 'mismatch'],
    'producer_snapshot': ['unavailable', 'missing', 'within-window', 'expired', 'observation-unknown'],
    'producer_standby': ['unavailable', 'off', 'on', 'stopping', 'unknown'],
    'producer_registration': ['unavailable', 'disconnected', 'connecting', 'authenticated', 'unknown'],
    'producer_work': ['unavailable', 'idle', 'preparing', 'running', 'stopping', 'unknown'],
    'producer_cleanup': ['unavailable', 'confirmed', 'pending', 'unknown'],
    'producer_transition': ['unavailable', 'missing', 'other-request', 'pending', 'applied', 'rejected', 'unknown'],
}
RUN_ALLOW_SUBSTEPS = [
    'producer-state-wait', 'allow-request', 'allow-receipt-wait',
    'extend-request', 'extend-receipt-wait',
]
GATEWAY_SETUP_SUBSTEPS = [
    'root-stat', 'ca-key-generate', 'ca-certificate-create', 'ca-certificate-parse',
    'server-key-generate', 'server-certificate-create', 'host-key-generate',
    'host-signer-create', 'principal-key-generate', 'principal-signer-create',
    'other-principal-key-generate', 'other-principal-signer-create', 'jwt-key-generate',
    'listener-bind', 'jwks-encode', 'verifier-create', 'gateway-create', 'handler-create',
    'hosts-configure', 'resident-configure', 'host-key-encode', 'host-key-file-write',
    'policy-encode', 'policy-file-write',
]
WORK_RESPONSE_SUBSTEPS = ['timeout', 'eof', 'invalid-json', 'shape-mismatch']
FAILURE_SUBSTEPS = ['unavailable', *RUN_ALLOW_SUBSTEPS, *GATEWAY_SETUP_SUBSTEPS,
                    *WORK_RESPONSE_SUBSTEPS]
FAILURE_DIAGNOSTIC_FIELDS = (
    'failure_substep', 'failure_elapsed_ms', 'failure_deadline_ms', 'failure_iteration',
)


def project(changes=None, trigger='observer'):
    source = (ROOT / '.github/workflows/producer-connection.yml').read_text()
    source = textwrap.dedent(source.split("<<'PYFAIL'\n", 1)[1].split('\n          PYFAIL', 1)[0])
    with tempfile.TemporaryDirectory() as tmp:
        report = Path(tmp) / 'result.json'
        record = dict(invocation='1' * 32, test='TestCIActualProducerConnection',
                      helper_sha256='2' * 64, outcome='failed-unknown', cleanup='unconfirmed',
                      stage='off-confirm', failure_code='producer-observation-deadline')
        record.update(changes or {})
        report.write_text(json.dumps(record))
        env = dict(LANE='normal', CI_PRODUCER_ADAPTER='json', CI_PRODUCER_SHA256='2' * 64,
                   CI_PRODUCER_SOURCE_SHA='3' * 40, CI_PRODUCER_INVOCATION='1' * 32,
                   CI_PRODUCER_RESULT=str(report), GITHUB_STEP_SUMMARY=str(Path(tmp) / 'summary'))
        output = io.StringIO()
        with patch.dict(os.environ, env, clear=True), patch('sys.argv', ['-', 'connection', 'TestCIActualProducerConnection', trigger]), contextlib.redirect_stdout(output):
            exec(compile(source, '<workflow diagnostic projection>', 'exec'), {})
        return json.loads(output.getvalue())


class ProducerDiagnosticProjectionTests(unittest.TestCase):
    def test_fixed_receipt_distinguishes_off_refusal_without_changing_failure(self):
        for receipt in ('missing', 'accepted', 'stale', 'rejected', 'unknown', 'mismatch'):
            record = dict.fromkeys(FIELDS, 'unavailable')
            record.update(producer_request_intent='off', producer_receipt=receipt,
                          producer_snapshot='expired', producer_transition='pending')
            result = project(record)
            self.assertEqual(result.get('producer_receipt'), receipt)
            self.assertEqual(result.get('producer_snapshot'), 'expired')
            self.assertEqual(result.get('producer_transition'), 'pending')
            self.assertEqual(result['failure_code'], 'producer-observation-deadline')
            self.assertEqual(result['outcome'], 'failed-unknown')
            self.assertIsNone(result['original_test_exit'])
            self.assertEqual(result['observer_verdict'], 'unavailable')

    def test_contract_and_workflow_allow_every_fixed_value(self):
        contract = json.loads((ROOT / 'ci-producer/DIAGNOSTIC_ENUMS.json').read_text())
        self.assertEqual(contract.get('producer_fields'), FIELDS)
        source = (ROOT / 'ci-producer/fixtures/ci_diagnostic_test.go').read_text()
        block = source.split('var ciProducerEnums = map[string][]string{', 1)[1].split('\n}', 1)[0]
        go_fields = {field: ['unavailable', *re.findall(r'"([^"]+)"', values)]
                     for field, values in re.findall(r'"([^"]+)":\s*\{([^}]+)\}', block)}
        self.assertEqual(go_fields, FIELDS)
        for field, choices in FIELDS.items():
            for choice in choices:
                record = {**dict.fromkeys(FIELDS, 'unavailable'), field: choice}
                self.assertEqual(project(record).get(field), choice, (field, choice))

    def test_partial_or_corrupted_group_is_entirely_unavailable(self):
        valid = dict(producer_request_intent='off', producer_receipt='accepted',
                     producer_snapshot='within-window', producer_standby='off',
                     producer_registration='disconnected', producer_work='idle',
                     producer_cleanup='confirmed', producer_transition='applied')
        for field in FIELDS:
            for invalid in (None, 'RAW_PRIVATE_SENTINEL', 'MISSING'):
                record = dict(valid)
                if invalid == 'MISSING':
                    del record[field]
                else:
                    record[field] = invalid
                result = project(record)
                self.assertTrue(all(result.get(key) == 'unavailable' for key in FIELDS))
                self.assertNotIn('RAW_PRIVATE_SENTINEL', json.dumps(result))

    def test_absent_and_untrusted_values_never_publish_private_data(self):
        for value in (None, 17, True, [], {}, 'RAW_PRIVATE_SENTINEL'):
            result = project({field: value for field in FIELDS})
            for field in FIELDS:
                self.assertEqual(result.get(field), 'unavailable')
            self.assertNotIn('RAW_PRIVATE_SENTINEL', json.dumps(result))
        for changes, trigger in (({}, 'observer'), ({'invocation': '4' * 32}, 'observer'),
                                 ({'test': 'other'}, 'observer'), ({}, 'preflight')):
            changes = dict(changes)
            if changes or trigger == 'preflight':
                changes.update(dict.fromkeys(FIELDS, 'unknown'))
            result = project(changes, trigger)
            for field in FIELDS:
                self.assertEqual(result.get(field), 'unavailable')

    def test_failure_substeps_and_integer_diagnostics_project_by_code(self):
        cases = [('run-allow', substep, 'unavailable', 'unavailable', 'unavailable', 'unavailable')
                 for substep in ('allow-request', 'extend-request')]
        cases += [('gateway-setup', substep, 'unavailable', 'unavailable', 'unavailable', 'unavailable')
                  for substep in GATEWAY_SETUP_SUBSTEPS]
        cases += [('work-cli-response', substep, 37, 40000,
                   3 if substep == 'shape-mismatch' else 'unavailable',
                   'list' if substep == 'shape-mismatch' else 'get_execution')
                  for substep in WORK_RESPONSE_SUBSTEPS]
        cases += [('run-allow', 'producer-state-wait', 8, 10000, 'unavailable', 'unavailable'),
                  ('run-allow', 'allow-receipt-wait', 9, 10000, 'unavailable', 'unavailable'),
                  ('run-allow', 'extend-receipt-wait', 10, 10000, 'unavailable', 'unavailable'),
                  ('producer-observation-deadline', 'unavailable', 'unavailable', 'unavailable',
                   'unavailable', 'unavailable')]
        for code, substep, elapsed, deadline, iteration, operation in cases:
            with self.subTest(code=code, substep=substep):
                changes = dict(failure_code=code, failure_substep=substep,
                               failure_elapsed_ms=elapsed, failure_deadline_ms=deadline,
                               failure_iteration=iteration, tool_operation=operation)
                result = project(changes)
                self.assertEqual(result['failure_code'], code)
                self.assertEqual(tuple(result.get(field) for field in FAILURE_DIAGNOSTIC_FIELDS),
                                 (substep, elapsed, deadline, iteration))

    def test_failure_substeps_match_dictionary_and_go_fixture(self):
        contract = json.loads((ROOT / 'ci-producer/DIAGNOSTIC_ENUMS.json').read_text())
        self.assertEqual(set(contract.get('failure_fields', {}).get('failure_substep', [])),
                         set(FAILURE_SUBSTEPS))
        self.assertEqual(contract.get('integer_fields'), {
            'failure_elapsed_ms': {'minimum': 0},
            'failure_deadline_ms': {'minimum': 1},
            'failure_iteration': {'minimum': 1},
        })
        source = (ROOT / 'ci-producer/fixtures/ci_diagnostic_test.go').read_text()
        block = source.split('var ciFailureSubsteps = map[string][]string{', 1)[1].split('\n}', 1)[0]
        go_groups = {
            code: set(re.findall(r'"([^"]+)"', values))
            for code, values in re.findall(r'"([^"]+)":\s*\{([^}]+)\}', block)
        }
        self.assertEqual(go_groups, {
            'run-allow': set(RUN_ALLOW_SUBSTEPS),
            'gateway-setup': set(GATEWAY_SETUP_SUBSTEPS),
            'work-cli-response': set(WORK_RESPONSE_SUBSTEPS),
        })

    def test_failure_diagnostic_group_rejects_unknown_wrong_types_and_mismatched_codes(self):
        valid = dict(failure_code='work-cli-response', failure_substep='shape-mismatch',
                     failure_elapsed_ms=3, failure_deadline_ms=40000,
                     failure_iteration=2, tool_operation='list')
        invalid_cases = (
            {'failure_substep': 'RAW_PRIVATE_SENTINEL'},
            {'failure_elapsed_ms': '3'}, {'failure_deadline_ms': True},
            {'failure_iteration': 0}, {'failure_iteration': '2'},
            {'failure_substep': 'host-key-generate'},
            {'failure_substep': 'shape-mismatch', 'failure_iteration': 2,
             'tool_operation': 'get_execution'},
        )
        for changes in invalid_cases:
            with self.subTest(changes=changes):
                result = project({**valid, **changes})
                self.assertEqual(tuple(result.get(field) for field in FAILURE_DIAGNOSTIC_FIELDS),
                                 ('unavailable',) * 4)
                self.assertNotIn('RAW_PRIVATE_SENTINEL', json.dumps(result))
        incomplete = dict(valid)
        del incomplete['failure_elapsed_ms']
        result = project(incomplete)
        self.assertEqual(tuple(result.get(field) for field in FAILURE_DIAGNOSTIC_FIELDS),
                         ('unavailable',) * 4)

    def test_failure_substeps_are_unavailable_for_other_failure_codes(self):
        result = project(dict(failure_code='mcp-request', failure_substep='listener-bind',
                              failure_elapsed_ms=8, failure_deadline_ms=10000,
                              failure_iteration=1))
        self.assertEqual(tuple(result.get(field) for field in FAILURE_DIAGNOSTIC_FIELDS),
                         ('unavailable',) * 4)


if __name__ == '__main__':
    unittest.main()
