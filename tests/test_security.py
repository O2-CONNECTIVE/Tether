"""オフラインで行う異常系・セキュリティの回帰テスト。中継サーバー、SSH サーバー、認証情報は使用しない。"""
import base64
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from rescue_core import (RescueError, Settings, load_session_state, record_session_state,
    session_state_path, remove_session_state_if_name, parse_host_json, run_upterm,
    stop_session, get_session_info, validate_public_keys, version_from_output,
    require_secure_session_backend, upterm_cmd_start)
from rescue_watchdog import watchdog_main
from test_core import key_line

NAME = 'rescue-123456abcdef'
OTHER = 'rescue-000000000000'

class SecurityTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env = patch.dict(os.environ, {'XDG_DATA_HOME': str(self.root), 'LOCALAPPDATA': str(self.root)})
        self.env.start()
        self.addCleanup(self.env.stop)

    def test_release_gate_cannot_be_enabled_by_environment(self):
        with patch.dict(os.environ, {'RESCUE_ALLOW_UNSAFE': '1'}):
            with self.assertRaises(RescueError): require_secure_session_backend()

    def test_authorized_keys_options_rejected(self):
        for prefix in ('permitopen="127.0.0.1:22" ', 'restrict ', 'command="id" '):
            with self.assertRaises(RescueError): validate_public_keys(prefix + key_line())

    def test_truncated_oversized_trailing_key_blob(self):
        raw = base64.b64decode(key_line().split()[1])
        for bad in (raw[:-1], raw + b'extra', raw[:15] + b'\xff\xff\xff\xff' + raw[19:]):
            with self.assertRaises(RescueError):
                validate_public_keys('ssh-ed25519 ' + base64.b64encode(bad).decode())

    def test_non_string_keys(self):
        for bad in (None, [], 1):
            with self.assertRaises(RescueError): validate_public_keys(bad)

    def test_boolean_duration_rejected(self):
        with self.assertRaises(RescueError): Settings(duration_minutes=True, public_keys=key_line()).validate()

    def test_version_rejects_prerelease_and_confusing_metadata(self):
        for text in ('{"version":"v0.39.0-rc.1"}', '{"goVersion":"go1.25.0"}', '{"version":39}', 'go1.25.0 upterm v0.39.0'):
            with self.assertRaises(RescueError): version_from_output(text)

    def test_command_output_rejects_executable_options(self):
        for command in ('ssh -o ProxyCommand=touch%20/tmp/evil tok@host', 'ssh tok@host; touch /tmp/evil', 'ssh tok@host\nwhoami', 'ssh tok@host $(id)'):
            with self.assertRaises(RescueError): parse_host_json(json.dumps({'sshCommand':command}))

    def test_corrupt_state_is_not_absent_and_preserved(self):
        path = session_state_path(self.root); path.write_text('{')
        with self.assertRaises(RescueError): load_session_state(self.root)
        self.assertEqual(path.read_text(), '{')

    def test_nonfinite_missing_and_invalid_deadline_rejected(self):
        for deadline in (None, float('nan'), float('inf'), -1, True, '123'):
            with self.assertRaises(RescueError): record_session_state({'name':NAME, 'deadline':deadline},self.root)

    def test_session_command_not_persisted(self):
        record_session_state({'name':NAME,'deadline':100,'ssh_command':'ssh secret@host'},self.root)
        self.assertNotIn('secret',session_state_path(self.root).read_text())

    def test_cleanup_rejects_path_traversal(self):
        with self.assertRaises(RescueError): remove_session_state_if_name('../escape',self.root)

    def test_unknown_status_not_ended(self):
        cp = subprocess.CompletedProcess([],0,'{"status":"mystery"}','')
        with patch('rescue_core.run_upterm',return_value=cp):
            with self.assertRaises(RescueError): get_session_info(Path('fake'),NAME)

    def test_wrong_name_rejected(self):
        cp = subprocess.CompletedProcess([],0,json.dumps({'status':'ended','name':OTHER}),'')
        with patch('rescue_core.run_upterm',return_value=cp):
            with self.assertRaises(RescueError): get_session_info(Path('fake'),NAME)

    def test_stop_requires_confirmation_even_after_success(self):
        cp = subprocess.CompletedProcess([],0,'','')
        with patch('rescue_core.run_upterm',return_value=cp) as run, patch('rescue_core.get_session_info',return_value={'status':'ready'}):
            with self.assertRaises(RescueError): stop_session(Path('fake'),NAME,confirmation_timeout=0)
            self.assertEqual(run.call_args.kwargs['timeout'],65)

    def test_stop_lookup_failure_not_success(self):
        with patch('rescue_core.run_upterm',return_value=subprocess.CompletedProcess([],0,'','')), patch('rescue_core.get_session_info',side_effect=RescueError('bad')):
            with self.assertRaises(RescueError): stop_session(Path('fake'),NAME)

    def test_legacy_absence_is_not_confirmed_without_context(self):
        with patch('rescue_core.run_upterm',return_value=subprocess.CompletedProcess([],4,'','')):
            with self.assertRaises(RescueError): stop_session(Path('fake'),NAME)

    def test_config_and_environment_isolation(self):
        with patch.dict(os.environ, {'UPTERM_AUTHORIZED_USER':'github:attacker','UPTERM_ALLOW_LOCAL_TCP_FORWARDING':'true','XDG_CONFIG_HOME':'/untrusted'}), patch('rescue_core.subprocess.run',return_value=subprocess.CompletedProcess([],0,'','')) as run:
            run_upterm(Path('fake'),['version'])
            env=run.call_args.kwargs['env']
            self.assertFalse(any(k.startswith('UPTERM_') for k in env))
            self.assertNotEqual(env['XDG_CONFIG_HOME'],'/untrusted')
            config=Path(env['XDG_CONFIG_HOME'])/'upterm'/'config.yaml'
            self.assertEqual(json.loads(config.read_text()),{})

    def test_overdue_watchdog_stops_immediately(self):
        with patch('rescue_watchdog.resolve_upterm',return_value=Path('fake')), patch('rescue_watchdog.stop_session') as stop, patch('rescue_watchdog.remove_session_state_if_name') as clean, patch('rescue_watchdog.time.sleep') as sleep:
            self.assertEqual(watchdog_main(NAME,'1','fake'),0)
            stop.assert_called_once();clean.assert_called_once();sleep.assert_not_called()

    def test_dangling_state_symlink_not_absent(self):
        try:
            session_state_path(self.root).symlink_to(self.root/'missing')
        except OSError as exc:
            self.skipTest(f'Symlink creation unavailable: {exc}')
        with self.assertRaises(RescueError):load_session_state(self.root)

    def test_watchdog_nonfinite_deadlines_rejected(self):
        for value in ('nan','inf','-inf'):
            with patch('rescue_watchdog.resolve_upterm') as resolve:
                self.assertEqual(watchdog_main(NAME,value,'fake'),2)
                resolve.assert_not_called()

    def test_watchdog_failure_retains_recovery(self):
        with patch('rescue_watchdog.resolve_upterm',return_value=Path('fake')), patch('rescue_watchdog.stop_session',side_effect=RescueError('failed')),patch('rescue_watchdog.remove_session_state_if_name') as clean,patch('rescue_watchdog.time.sleep'):
            self.assertEqual(watchdog_main(NAME,'1','fake'),1)
            clean.assert_not_called()

    def test_clock_rollback_does_not_extend_watchdog(self):
        with patch('rescue_watchdog.resolve_upterm',return_value=Path('fake')),patch('rescue_watchdog.time.time',side_effect=[100,100,90]),patch('rescue_watchdog.time.monotonic',side_effect=[1000,1011]),patch('rescue_watchdog.stop_session') as stop,patch('rescue_watchdog.remove_session_state_if_name'),patch('rescue_watchdog.time.sleep') as sleep:
            self.assertEqual(watchdog_main(NAME,'110','fake'),0)
            stop.assert_called_once();sleep.assert_not_called()

if __name__=='__main__':unittest.main()
