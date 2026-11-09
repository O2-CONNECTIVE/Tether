"""Finite script-only regressions. No Go binary, socket, or producer is run."""
import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import runpy
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent

class PreparationTests(unittest.TestCase):
    def test_generator_absent_files_and_targets_fail_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            backend = root / 'backend'
            source = backend / 'internal/residentpipe/run_darwin.go'
            source.parent.mkdir(parents=True)
            raw = b'owner, e := residentmac.LoadStandby(ctx, protectedPolicy)\n'
            source.write_bytes(raw)
            # Copy source bytes only for the two independently pinned diagnostic seams.
            # This regression executes Python generator logic, never Go/native code.
            for relative in ('internal/macagent/process.go', 'internal/agentexec/run.go', 'cmd/tether-host/entry.go'):
                target = backend / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes((HERE.parent / 'backend' / relative).read_bytes())
            (backend / 'go.mod').write_text('fixture only\n')
            script = root / 'prepare_overlay.py'
            text = (HERE / 'prepare_overlay.py').read_text()
            import re
            text = re.sub(r"SOURCE_SHA = '[a-f0-9]+'", "SOURCE_SHA = '"+hashlib.sha256(raw).hexdigest()+"'", text)
            script.write_text(text)
            fixtures = root / 'fixtures'
            fixtures.mkdir()
            names = [p.name for p in (HERE / 'fixtures').glob('*.go')]
            def execute():
                with patch('sys.argv', [str(script), '--backend', str(backend)]), contextlib.redirect_stdout(io.StringIO()):
                    runpy.run_path(str(script), run_name='__main__')
            with self.assertRaisesRegex(SystemExit, 'Required fixture missing'):
                execute()
            for name in names:
                (fixtures / name).write_text('// synthetic generator unit fixture\n')
            execute()
            transformed = (root / 'generated/run_darwin.go').read_bytes()
            self.assertEqual(transformed.replace(b'ciLoadStandby(ctx, protectedPolicy)', b'residentmac.LoadStandby(ctx, protectedPolicy)'), raw)
            target = backend / 'internal/agenttransport/ci_producer_transport.go'
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text('must not overwrite\n')
            with self.assertRaisesRegex(SystemExit, 'Virtual fixture target already exists'):
                execute()
            self.assertEqual(target.read_text(), 'must not overwrite\n')

    def test_cli_overlay_uses_same_main_and_rejects_changed_gate(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            backend = root / 'backend'
            for relative in ('internal/residentpipe/run_darwin.go', 'internal/macagent/process.go',
                             'internal/agentexec/run.go', 'cmd/tether-host/main.go', 'cmd/tether-host/entry.go', 'go.mod'):
                target = backend / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes((HERE.parent / 'backend' / relative).read_bytes())
            script = root / 'prepare_overlay.py'
            script.write_bytes((HERE / 'prepare_overlay.py').read_bytes())
            fixtures = root / 'fixtures'
            fixtures.mkdir()
            for source in (HERE / 'fixtures').glob('*.go'):
                (fixtures / source.name).write_bytes(source.read_bytes())
            def execute():
                with patch('sys.argv', [str(script), '--backend', str(backend)]), contextlib.redirect_stdout(io.StringIO()):
                    runpy.run_path(str(script), run_name='__main__')
            execute()
            mapping = json.loads((root / 'OVERLAY.json').read_text())['Replace']
            self.assertNotIn(str(backend / 'cmd/tether-host/main.go'), mapping)
            self.assertEqual(mapping[str(backend / 'cmd/tether-host/entry.go')], str(fixtures / 'ci_cli_entry.go'))
            self.assertNotIn(str(backend / 'internal/residentpipe/foreground.go'), mapping)
            self.assertNotIn(str(backend / 'internal/residentpipe/foreground_darwin.go'), mapping)
            (backend / 'cmd/tether-host/entry.go').write_text('// altered composition\n')
            with self.assertRaisesRegex(SystemExit, 'Production CLI entry changed'):
                execute()

    def test_keeper_refuses_mismatch_and_stale_without_spawning(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            report = root / 'result.json'
            argv = ['supervise_ci.py', 'keeper', '--report', str(report), '--invocation', '1'*32, '--test', str(root/'test'), '--log', str(root/'log')]
            env = {'CI_ACTUAL_PRODUCER':'1','RUNNER_ENVIRONMENT':'github-hosted','RUNNER_OS':'macOS','RUNNER_ARCH':'ARM64','CI_PRODUCER_INVOCATION':'1'*32,'CI_PRODUCER_RESULT':str(root/'wrong')}
            def execute():
                with patch('sys.argv', argv), patch.dict(os.environ, env, clear=True):
                    runpy.run_path(str(HERE/'supervise_ci.py'),run_name='__main__')
            with patch('subprocess.Popen') as spawn:
                with self.assertRaisesRegex(SystemExit, 'does not match'):
                    execute()
                env['CI_PRODUCER_RESULT'] = str(report)
                report.write_text('{}')
                with self.assertRaisesRegex(SystemExit, 'stale outcome'):
                    execute()
                spawn.assert_not_called()

    def test_observer_rejects_unconfirmed_supervisor_result(self):
        with tempfile.TemporaryDirectory() as tmp:
            report = Path(tmp)/'result.json'
            report.with_suffix('.supervisor.json').write_text(json.dumps({'outcome':'passed','original_test_exit':0,'cleanup':'unconfirmed','invocation':'1'*32,'test':'TestCIActualProducerConnection'}))
            env = {'CI_ACTUAL_PRODUCER':'1','RUNNER_ENVIRONMENT':'github-hosted','RUNNER_OS':'macOS','RUNNER_ARCH':'ARM64','CI_PRODUCER_INVOCATION':'1'*32}
            with patch('sys.argv',['supervise_ci.py','observer','--report',str(report),'--invocation','1'*32]), patch.dict(os.environ,env,clear=True), patch('time.monotonic',side_effect=[0,1,901]), patch('time.sleep'), patch('subprocess.Popen') as spawn:
                with self.assertRaisesRegex(SystemExit,'timed out'):
                    runpy.run_path(str(HERE/'supervise_ci.py'),run_name='__main__')
                spawn.assert_not_called()


    def test_observer_ignores_stale_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            report = Path(tmp)/'result.json'
            report.with_suffix('.supervisor.json').write_text(json.dumps({'outcome':'passed','original_test_exit':0,'cleanup':'confirmed','invocation':'2'*32,'test':'TestCIActualProducerConnection'}))
            env = {'CI_ACTUAL_PRODUCER':'1','RUNNER_ENVIRONMENT':'github-hosted','RUNNER_OS':'macOS','RUNNER_ARCH':'ARM64','CI_PRODUCER_INVOCATION':'1'*32}
            with patch('sys.argv',['supervise_ci.py','observer','--report',str(report),'--invocation','1'*32]), patch.dict(os.environ,env,clear=True), patch('time.monotonic',side_effect=[0,1,901]), patch('time.sleep'), patch('subprocess.Popen') as spawn:
                with self.assertRaisesRegex(SystemExit,'timed out'):
                    runpy.run_path(str(HERE/'supervise_ci.py'),run_name='__main__')
                spawn.assert_not_called()

if __name__ == '__main__':
    unittest.main()
