import os
from pathlib import Path
import shutil
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'src'))
from rescue_core import (
    RescueError, Settings, check_upterm_version, get_session_info, parse_host_json,
    prepare_authorized_keys, record_session_state, resolve_known_hosts, run_upterm,
    session_state_path, stop_session, upterm_cmd_start,
)
from test_core import key_line

NAME = 'rescue-0123456789ab'


@unittest.skipIf(sys.platform == 'win32', 'This mocked shebang executable is only used on Unix CI')
class TestIntegration(unittest.TestCase):
    def test_start_and_stop_using_fake_upterm(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            fake = state / 'upterm'
            shutil.copy2(Path(__file__).parent / 'fake_upterm.py', fake)
            fake.chmod(0o755)
            settings = Settings(
                mode='terminal', duration_minutes=5, public_keys=key_line(),
                external_upterm=str(fake),
            )
            # 偽の Upterm 実行ファイルを core 経由の subprocess で実際に起動する。
            # 開始ゲート（require_secure_session_backend）は core の下位関数にはなく、ここでは通らない。
            with patch.dict(os.environ, {'XDG_DATA_HOME': str(state), 'LOCALAPPDATA': str(state), 'FAKE_UPTERM_STATE': str(state / 'fake')}):
                check_upterm_version(fake)
                key_path = prepare_authorized_keys(settings, NAME)
                known_hosts = resolve_known_hosts(settings)
                record_session_state({'name': NAME, 'deadline': time.time() + 300, 'external_upterm': str(fake)})
                result = run_upterm(fake, upterm_cmd_start(settings, NAME, known_hosts, key_path), timeout=50)
                self.assertEqual(result.returncode, 0)
                self.assertIn('TokenMadeUp', parse_host_json(result.stdout)['ssh_command'])
                self.assertEqual(get_session_info(fake, NAME)['status'], 'ready')
                self.assertTrue(session_state_path().exists())
                with self.assertRaises(RescueError):
                    stop_session(fake, NAME)
                self.assertEqual(get_session_info(fake, NAME)['status'], 'ended')
                self.assertTrue(session_state_path().exists())


if __name__ == '__main__':
    unittest.main()
