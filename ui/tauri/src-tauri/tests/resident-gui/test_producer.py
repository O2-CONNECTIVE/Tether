"""Pure source-frame regression. Does not launch a child, native IO or WebView."""
import json
from pathlib import Path
import runpy
import unittest

source = runpy.run_path(str(Path(__file__).with_name('producer.py')))
frame = source['status_frame']


class HeartbeatContract(unittest.TestCase):
    def test_emit_permission_is_only_in_explicit_fixture_context(self):
        root = Path(__file__).parents[2]
        normal = json.loads((root / 'tauri.conf.json').read_text())
        self.assertEqual(normal['app']['security']['capabilities'], ['shell-local'])
        shell = json.loads((root / 'capabilities/shell-local.json').read_text())
        self.assertNotIn('core:event:allow-emit', shell['permissions'])
        fixture = json.loads(Path(__file__).with_name('tauri.conf.json').read_text())
        capability, = fixture['app']['security']['capabilities']
        self.assertEqual(capability['windows'], ['main'])
        self.assertTrue(capability['local'])
        self.assertNotIn('remote', capability)
        self.assertEqual(capability['permissions'], ['allow-shell-status', 'allow-resident-port', 'core:event:allow-emit'])

    def test_stale_request_is_not_accepted(self):
        self.assertEqual(source['disposition']({'expectedRevision': 1}, 2), 'stale')
        self.assertEqual(source['disposition']({'expectedRevision': 2}, 2), 'accepted')

    def test_refreshed_deadline_requires_new_revision(self):
        state = dict(revision=0, validForMs=3000)
        frames = [json.loads(frame(state, t)) for t in (100, 300, 500)]
        self.assertEqual([x['snapshot']['revision'] for x in frames], [1, 2, 3])
        self.assertEqual([x['sourceContinuousMs'] for x in frames], [100, 300, 500])
        self.assertTrue(all(x['snapshot']['validForMs'] == 3000 for x in frames))
        self.assertTrue(all(x['protocolVersion'] == 1 and x['type'] == 'status' for x in frames))


if __name__ == '__main__':
    unittest.main()
