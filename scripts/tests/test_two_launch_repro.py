"""External control fixtures, not an actual Windows reproduction."""
import sys
import unittest
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from two_launch_repro import PublicationView, serial

class ControlTests(unittest.TestCase):
    def test_closed_view(self):
        view = PublicationView(Path('setup'), Path('trial'))
        self.assertEqual(view / 'unpacked/BUILD.json', Path('setup/unpacked/BUILD.json'))
        self.assertEqual(view / 'overlay.json', Path('setup/overlay.json'))
        for name in ('steps.json', 'evidence/native-result.json', 'evidence/native-observation.json',
                     'tools/app-startup/wry-completion-probe.json', 'tools/app-startup/wry-sdk-delivery.json',
                     'tools/app-startup/wry-receiver-polls.json', 'tools/app-startup/wry-pump-brackets.json',
                     'tools/driver-stage.jsonl', 'evidence/app-startup-report.json', 'launch.json', 'final.json'):
            self.assertEqual(view / name, Path('trial') / name)
        for name in ('../setup', 'tools', 'unpacked/rescue-host-shell.exe', '/tmp/x'):
            with self.assertRaises(ValueError): view / name

    def test_stop_and_cap(self):
        for outcome in ('failure', 'unknown', 'malformed', 'cancelled'):
            calls = []
            self.assertEqual(serial(lambda n: calls.append(n) or outcome), outcome)
            self.assertEqual(calls, [1])
        calls = []
        self.assertEqual(serial(lambda n: calls.append(n) or 'healthy'), 'mechanical-reuse-demonstrated')
        self.assertEqual(calls, [1, 2])
        calls = []
        def fail(n):
            calls.append(n)
            raise ValueError()
        self.assertEqual(serial(fail), 'unknown')
        self.assertEqual(calls, [1])

class StagingTests(unittest.TestCase):
    def test_staging_is_fresh_whitelisted_and_byte_bound(self):
        import tempfile
        import hashlib
        from types import SimpleNamespace
        from two_launch_repro import stage, hashes
        source = SimpleNamespace(digest=lambda b: hashlib.sha256(b).hexdigest())
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            setup, trial = root / 'setup', root / 'trial'
            (setup / 'tools').mkdir(parents=True)
            (setup / 'tools/a').write_bytes(b'original')
            (setup / 'tools/old.finish').write_bytes(b'stale')
            expected = stage(setup, trial, ['a'], source)
            self.assertEqual(list((trial / 'tools').iterdir()), [trial / 'tools/a'])
            with self.assertRaises(FileExistsError): stage(setup, trial, ['a'], source)
            (trial / 'tools/a').write_bytes(b'drift')
            self.assertNotEqual(hashes({'a': trial / 'tools/a'}, source), expected)
            (setup / 'tools/a').write_bytes(b'setup-drift')
            self.assertNotEqual(hashes({'a': setup / 'tools/a'}, source), expected)
            stale = root / 'stale'
            stale.mkdir()
            (stale / 'finish').touch()
            with self.assertRaises(FileExistsError): stage(setup, stale, ['a'], source)

    def test_real_frozen_publisher_uses_only_closed_routes(self):
        import contextlib
        import io
        import os
        from unittest.mock import patch
        import wry_probe_ci as source
        seen = []
        def read(path, *args):
            seen.append(path)
            raise ValueError('fixture unavailable')
        with patch.dict(os.environ, {'CONTROL_HEAD': 'a'*40, 'WRY_RECEIVER_ENABLED': '1', 'WRY_PUMP_ENABLED': '1'}), \
             patch.object(source, 'load', side_effect=read), \
             patch.object(source, 'git', return_value=b'a'*40), contextlib.redirect_stdout(io.StringIO()):
            self.assertFalse(source.publish(Path('source'), PublicationView(Path('setup'), Path('trial')), True))
        self.assertIn(Path('setup/unpacked/BUILD.json'), seen)
        self.assertIn(Path('setup/overlay.json'), seen)
        for name in ('steps.json', 'launch.json', 'final.json', 'evidence/native-result.json',
                     'evidence/native-observation.json', 'evidence/app-startup-report.json'):
            self.assertIn(Path('trial') / name, seen)
        self.assertTrue(all(p.parts[0] == ('setup' if str(p).split('/',1)[1] in
                                         ('unpacked/BUILD.json', 'overlay.json') else 'trial') for p in seen))

    def test_rejected_raw_conditions_cannot_invoke_again(self):
        from copy import deepcopy
        from two_launch_readiness import eligibility, snapshot_summary
        from test_two_launch_readiness import healthy, PATHS
        ready = snapshot_summary([], [], PATHS)
        mutations = []
        for key, value in [('nativeTestsPassed', 0), ('normalCloseVerified', 'true'),
                           ('cleanupFailures', 1), ('appExitCode', None)]:
            raw = healthy(); raw[key] = value; mutations.append((raw, 0, ready))
        for key, value in [('observed', 0), ('omittedRows', 1), ('rows', [])]:
            raw = healthy(); raw['afterDriverCleanupFacts'][key] = value; mutations.append((raw, 0, ready))
        for key, value in [('snapshotState', 'same-identity'), ('handleIdentityVerified', False)]:
            raw = healthy(); raw['childCloseFacts']['rows'][0][key] = value
            mutations.append((raw, 0, ready))
        mutations.extend([(healthy(), 1, ready), (healthy(), 0, snapshot_summary([], [4444], PATHS)),
                          (healthy(), 0, snapshot_summary([], [], PATHS, process_ok=False))])
        for raw, exit_code, snapshot in mutations:
            calls = []
            def invoke(n):
                calls.append(n)
                return 'healthy' if eligibility(deepcopy(raw), exit_code, snapshot)['eligible'] else 'unknown'
            self.assertEqual(serial(invoke), 'unknown')
            self.assertEqual(calls, [1])

    def test_identity_reads_staged_driver_and_stable_app(self):
        import tempfile
        import hashlib
        from types import SimpleNamespace
        from two_launch_repro import identity
        import bounded_pump_capture as control
        source = SimpleNamespace(permissions=lambda *a: None,
                 source_identity=lambda *a: {'sourceCommit': control.SOURCE, 'sourceTree': control.TREE},
                 digest=lambda b: hashlib.sha256(b).hexdigest(), safe_meta=lambda value, **kw: value)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve(); setup = root / 'setup'; trial = root / 'trial'
            paths = ['setup/unpacked/rescue-host-shell.exe', 'setup/unpacked/BUILD.json',
                     'setup/fixture/rescue-host-shell.exe', 'setup/fixture/BUILD.json', 'setup/fixture.zip',
                     'trial/tools/tauri/bin/tauri-driver.exe', 'trial/tools/edge/msedgedriver.exe',
                     'trial/tools/tauri-driver-2.1.0/Cargo.lock', 'tools/native-driver-trace/driver.patch']
            for name in paths:
                path = root / name; path.parent.mkdir(parents=True, exist_ok=True); path.write_bytes(b'fixed')
            before = identity(root, setup, trial, source)
            (trial / 'tools/edge/msedgedriver.exe').write_bytes(b'drift')
            self.assertNotEqual(identity(root, setup, trial, source), before)
            (setup / 'unpacked/rescue-host-shell.exe').write_bytes(b'drift')
            with self.assertRaises(ValueError): identity(root, setup, trial, source)

    def test_workflow_has_one_bounded_entry(self):
        text = (Path(__file__).resolve().parents[2] / '.github/workflows/two-launch-repro.yml').read_text()
        self.assertEqual(text.count('runs-on: windows-2022'), 1)
        self.assertEqual(text.count('two_launch_repro.py'), 1)
        for required in ('timeout-minutes: 35', "types: [opened]", 'github.run_attempt == 1',
                         'github.event.repository.private == false', 'github.event.pull_request.draft == true',
                         'github.event.pull_request.head.repo.full_name == github.repository',
                         "3bd7ee59dedd38728b731994a4088bd7e8c0a39d", 'contents: read'):
            self.assertIn(required, text)
        for forbidden in ('workflow_dispatch', 'upload-artifact', 'strategy:', 'secrets.', 'actions/cache'):
            self.assertNotIn(forbidden, text)

    def test_orchestration_spy_stops_after_raw_failure(self):
        import contextlib
        import io
        import tempfile
        import time
        from types import SimpleNamespace
        from unittest.mock import patch
        import two_launch_repro as pilot
        from test_two_launch_readiness import healthy, PATHS
        from two_launch_readiness import snapshot_summary
        for good in (False, True):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve(); setup = root / 'setup'; setup.mkdir()
                raw = healthy()
                if not good: raw['afterDriverCleanupFacts']['rows'] = []
                def load(path, *args):
                    if path.name == 'setup-start.json': return round(time.time()*1000)
                    if path.name == 'steps.json': return []
                    if path.name == 'native-result.json': return raw
                    return {}
                source = SimpleNamespace(load=load, safe_steps=lambda rows: rows, digest=lambda data: 'a'*64)
                calls = []
                def run(args, cwd, output=None):
                    if any(str(a).endswith('supervise.ps1') for a in args): calls.append(args)
                    return 0
                def stage(setup, trial, names, source): trial.mkdir(); return {}
                with patch.object(pilot.control, 'admit'), patch.object(pilot.control, 'receipt', return_value={}), \
                     patch.object(pilot, 'immutable_tools', return_value=()), patch.object(pilot, 'hashes', return_value={}), \
                     patch.object(pilot, 'stage', side_effect=stage), patch.object(pilot, 'identity', return_value={}), \
                     patch.object(pilot, 'regular', return_value=b'raw'), patch.object(pilot, 'run', side_effect=run), \
                     patch.object(pilot, 'snapshot', return_value=snapshot_summary([], [], PATHS)), \
                     patch.object(pilot, 'publish', return_value=(True, {})), contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(pilot.execute(root, setup, source, root), 0 if good else 1)
                self.assertEqual(len(calls), 2 if good else 1)

class FailureClassificationTests(unittest.TestCase):
    def test_target_red_needs_strict_counts_and_bound_diagnostic(self):
        import copy
        import os
        from unittest.mock import patch
        from two_launch_repro import failure_classification
        import bounded_pump_capture as control
        raw = {'nativeTestsRun': 1, 'nativeTestsPassed': 0}
        observation = {'status': 'failed', 'phase': 'session-create', 'errorClass': 'timeout', 'documentObservations': 0,
                       'elapsedMs': 15009, 'droppedRequests': 0,
                       'sessionRequests': [{'outcome': 'started', 'elapsedMs': 0, 'httpStatus': None},
                                           {'outcome': 'timeout', 'elapsedMs': 15004, 'httpStatus': None}]}
        launch = {'sourceCommit': control.SOURCE, 'sourceTree': control.TREE,
                  'exeSha256': 'e'*64, 'overlayLockSha256': 'a'*64}
        body = {'sourceCommit': control.SOURCE, 'sourceTree': control.TREE, 'controlCommit': 'c'*40,
                'sourceAndSpecimenStable': True, 'allOriginalAndDiagnosticChecksPassed': False,
                'files': {'native': {'data': raw}, 'observation': {'data': observation},
                          'launch': {'data': launch}, 'final': {'data': launch},
                          'build': {'data': {'sourceCommit': control.SOURCE, 'exeSha256': 'e'*64}},
                          'overlay': {'data': {'overlayLockSha256': 'a'*64}}}}
        with patch.dict(os.environ, {'CONTROL_HEAD': 'c'*40}):
            result = failure_classification(raw, observation, body, True, launch, 1)
            self.assertEqual(result['outcome'], 'target-red')
            self.assertTrue(result['observedSessionTimeout'])
            for final_ok, diagnostic in [(False, body), (True, None)]:
                result = failure_classification(raw, observation, diagnostic, final_ok, launch, 1)
                self.assertTrue(result['observedSessionTimeout'])
                self.assertFalse(result['targetRedBound'])
                self.assertEqual(result['outcome'], 'failure-evidence-inconclusive')
            for field in ('sourceCommit', 'sourceTree', 'controlCommit', 'sourceAndSpecimenStable'):
                bad = copy.deepcopy(body); bad[field] = None
                self.assertFalse(failure_classification(raw, observation, bad, True, launch, 1)['targetRedBound'])
            for section in ('native', 'observation', 'launch', 'final', 'build', 'overlay'):
                bad = copy.deepcopy(body); bad['files'][section]['data'] = {}
                self.assertFalse(failure_classification(raw, observation, bad, True, launch, 1)['targetRedBound'])
            self.assertEqual(failure_classification({}, {}, None, False, launch, 1)['outcome'],
                             'failure-evidence-inconclusive')
            import wry_probe_ci as source
            partial = source.observation_summary({'requests': []})
            candidates = [partial]
            for key, value in [('status', 'unknown'), ('phase', 'unknown'), ('documentObservations', None),
                               ('documentObservations', False), ('elapsedMs', '1'), ('droppedRequests', True)]:
                malformed = copy.deepcopy(observation); malformed[key] = value; candidates.append(malformed)
            malformed = copy.deepcopy(observation)
            malformed['sessionRequests'][0]['elapsedMs'] = False; candidates.append(malformed)
            for malformed in candidates:
                diagnostic = copy.deepcopy(body); diagnostic['files']['observation']['data'] = malformed
                with self.subTest(observation=malformed):
                    self.assertEqual(failure_classification(raw, malformed, diagnostic, True, launch, 1)['outcome'],
                                     'failure-evidence-inconclusive')
            other = copy.deepcopy(observation); other.update(phase='readiness', errorClass='other')
            diagnostic = copy.deepcopy(body); diagnostic['files']['observation']['data'] = other
            self.assertEqual(failure_classification(raw, other, diagnostic, True, launch, 1)['outcome'],
                             'other-native-failure')
            for counts in [(True, False), (1, False), (True, 0)]:
                bad = dict(zip(('nativeTestsRun', 'nativeTestsPassed'), counts))
                result = failure_classification(bad, observation, body, True, launch, 1)
                self.assertFalse(result['observedSessionTimeout'])
                self.assertNotEqual(result['outcome'], 'target-red')
