"""Synthetic control fixtures only, not Windows cleanup evidence."""
import copy
import json
from pathlib import Path
import sys
import unittest
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import two_launch_readiness as gate

PATHS = [r'C:\pilot\rescue-host-shell.exe', r'C:\trial\tauri-driver.exe',
         r'C:\trial\msedgedriver.exe']


def healthy():
    facts = {'observed': 2, 'remainingSnapshot': 0, 'omittedRows': 0,
             'rows': [{'slot': n, 'handleIdentityVerified': True,
                       'handleState': 'exited', 'snapshotState': 'absent'} for n in (1, 2)]}
    return {**dict.fromkeys(gate.TRUE_FIELDS, True),
            'nativeTestsRun': 1, 'nativeTestsPassed': 1, 'appExitCode': 0,
            'cleanupFailures': 0, 'childHandleDisposeFailures': 0,
            'childCloseFacts': copy.deepcopy(facts), 'afterDriverCleanupFacts': facts}


class ReadinessTests(unittest.TestCase):
    def ready(self):
        return gate.snapshot_summary([], [], PATHS)

    def test_positive_fixture_is_bounded_and_redacted(self):
        result = gate.eligibility(healthy(), 0, self.ready())
        self.assertTrue(result['eligible'])
        self.assertTrue(self.ready()['eligible'])
        self.assertEqual(set(self.ready()), gate.SNAPSHOT_KEYS)
        self.assertEqual(result['recordedChildrenCount'], 2)
        self.assertTrue(all(type(v) in (bool, int) for v in result.values()))
        self.assertNotIn('C:', json.dumps(result))

    def test_original_verdict_fields_are_strict(self):
        for key in gate.TRUE_FIELDS:
            for value in (False, 1, 'true', None):
                with self.subTest(key=key, value=value):
                    data = healthy(); data[key] = value
                    self.assertFalse(gate.eligibility(data, 0, self.ready())['eligible'])
        for key, expected in [('nativeTestsRun', 1), ('nativeTestsPassed', 1),
                              ('appExitCode', 0), ('cleanupFailures', 0),
                              ('childHandleDisposeFailures', 0)]:
            for value in (True, False, str(expected), None, expected + 1):
                data = healthy(); data[key] = value
                self.assertFalse(gate.eligibility(data, 0, self.ready())['eligible'])
        for exit_code in (None, True, False, '0', -1, 1):
            self.assertFalse(gate.eligibility(healthy(), exit_code, self.ready())['eligible'])

    def test_child_counts_missing_zero_omission_and_contradiction(self):
        for field in ('childCloseFacts', 'afterDriverCleanupFacts'):
            for value in (None, {}, [], 'private'):
                data = healthy(); data[field] = value
                self.assertFalse(gate.eligibility(data, 0, self.ready())['eligible'])
            for key, values in [('observed', (0, 1, 3, 33, True, '2', None)),
                                ('omittedRows', (1, True, '0', None)),
                                ('remainingSnapshot', (1, False, '0', None)),
                                ('rows', ([], None, [{}], [healthy()[field]['rows'][0]] * 2))]:
                for value in values:
                    data = healthy(); data[field][key] = value
                    self.assertFalse(gate.eligibility(data, 0, self.ready())['eligible'])

    def test_child_rows_slots_identity_liveness_and_types(self):
        for field in ('childCloseFacts', 'afterDriverCleanupFacts'):
            for key, values in [('slot', (0, 2, 3, True, '1', None)),
                                ('handleIdentityVerified', (1, 'true', None)),
                                ('handleState', ('invalid', [], None)),
                                ('snapshotState', ('invalid', [], None))]:
                for value in values:
                    data = healthy(); data[field]['rows'][0][key] = value
                    self.assertFalse(gate.eligibility(data, 0, self.ready())['eligible'])
            data = healthy(); data[field]['rows'].reverse()
            self.assertTrue(gate.eligibility(data, 0, self.ready())['eligible'])
        data = healthy()
        for field in ('childCloseFacts', 'afterDriverCleanupFacts'):
            data[field].update(observed=32, rows=[{**data[field]['rows'][0], 'slot': n}
                                                for n in range(1, 33)])
        self.assertTrue(gate.eligibility(data, 0, self.ready())['eligible'])

    def test_earlier_live_or_unverified_handle_is_not_final_exit_evidence(self):
        for state in ('live', 'exited', 'unavailable', 'different-identity'):
            for identity in (True, False):
                data = healthy()
                data['childCloseFacts']['rows'][0].update(
                    handleState=state, handleIdentityVerified=identity)
                self.assertTrue(gate.eligibility(data, 0, self.ready())['eligible'])
        for state in ('same-identity', 'pid-reused', 'ambiguous', 'absent'):
            data = healthy(); data['childCloseFacts']['rows'][0]['snapshotState'] = state
            self.assertTrue(gate.eligibility(data, 0, self.ready())['eligible'])
        for key, values in [('handleState', ('live', 'unavailable', 'different-identity')),
                            ('handleIdentityVerified', (False,)),
                            ('snapshotState', ('same-identity', 'pid-reused', 'ambiguous'))]:
            for value in values:
                data = healthy(); data['afterDriverCleanupFacts']['rows'][0][key] = value
                self.assertFalse(gate.eligibility(data, 0, self.ready())['eligible'])

    def test_snapshot_candidate_union_and_unrelated_missing_paths(self):
        for row in ({'Name': 'unrelated.exe', 'ExecutablePath': PATHS[0].upper()},
                    {'Name': 'TAURI-DRIVER.EXE', 'ExecutablePath': PATHS[1]},
                    {'Name': 'msedgedriver.exe', 'ExecutablePath': PATHS[2].replace('\\', '/')}):
            self.assertFalse(gate.snapshot_summary([row], [], PATHS)['matchedExecutablePathsAbsent'])
        rows = [{'Name': 'System', 'ExecutablePath': None},
                {'Name': 'msedgedriver.exe', 'ExecutablePath': r'C:\other\msedgedriver.exe'}]
        self.assertTrue(gate.snapshot_summary(rows, [], PATHS)['matchedExecutablePathsAbsent'])
        for row in ({'Name': None, 'ExecutablePath': PATHS[0]}, {'Name': ''},
                    {'Name': 'tauri-driver.exe'}, {'Name': ' '}, {'Name': 'bad\nname'}, {'Name': 'tauri-driver.exe', 'ExecutablePath': 7},
                    {'Name': 'tauri-driver.exe', 'ExecutablePath': 'relative.exe'}, {}, None):
            self.assertFalse(gate.snapshot_summary([row], [], PATHS)['snapshotComplete'])

    def test_snapshot_query_flags_bounds_listener_failures_and_ports(self):
        for flag in (False, None, 1, 'true'):
            self.assertFalse(gate.snapshot_summary([], [], PATHS, process_ok=flag)['snapshotComplete'])
            self.assertFalse(gate.snapshot_summary([], [], PATHS, listener_ok=flag)['portsVacant'])
        self.assertFalse(gate.snapshot_summary([{'Name': 'System'}]*4097, [], PATHS)['snapshotComplete'])
        for rows in (None, {}, 'private'):
            self.assertFalse(gate.snapshot_summary(rows, [], PATHS)['snapshotComplete'])
        for ports in ([4444], [4445], [True], [None], ['4444'], [0], [65536], [80]*4097, None):
            self.assertFalse(gate.snapshot_summary([], ports, PATHS)['portsVacant'])
        self.assertTrue(gate.snapshot_summary([], [80, 443], PATHS)['portsVacant'])
        for paths in ([], PATHS[:2], [PATHS[0]]*3, ['relative']*3, None):
            self.assertFalse(gate.snapshot_summary([], [], paths)['snapshotComplete'])

    def test_eligibility_rejects_malformed_summary_without_exposing_input(self):
        for summary in (None, {}, {'private': 'SECRET'}, []):
            result = gate.eligibility(healthy(), 0, summary)
            self.assertFalse(result['eligible']); self.assertNotIn('SECRET', json.dumps(result))
        for key in self.ready():
            for value in (None, 'true', 1 if type(self.ready()[key]) is bool else True):
                summary = self.ready(); summary[key] = value
                self.assertFalse(gate.eligibility(healthy(), 0, summary)['eligible'])
        self.assertFalse(gate.eligibility(None, 0, self.ready())['eligible'])
        summary = self.ready(); summary['eligible'] = False
        self.assertFalse(gate.eligibility(healthy(), 0, summary)['eligible'])
        summary = self.ready(); summary['private'] = 'SECRET'
        self.assertFalse(gate.eligibility(healthy(), 0, summary)['eligible'])


if __name__ == '__main__': unittest.main()
