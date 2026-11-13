"""Source/package regression checks, NOT Windows ownership or native acceptance."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('prepare_owned', HERE / 'prepare.py')
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


class SourceChecks(unittest.TestCase):
    def test_pinned_upstream_rejects_untrusted_bytes(self):
        with self.assertRaises(prepare.pinned.PatchError):
            prepare.pinned.upstream_files(b'untrusted')

    def test_patch_is_only_main_and_server(self):
        patch = (HERE / 'driver.patch').read_text()
        self.assertEqual([line for line in patch.splitlines() if line.startswith('+++ ')],
                         ['+++ b/src/main.rs', '+++ b/src/server.rs'])
        self.assertIn('owned_attach::start(channel, driver, outer)', patch)
        self.assertNotIn('tauri:options', '\n'.join(line for line in patch.splitlines() if line.startswith(('+', '-'))))

    def test_attach_has_one_session_and_no_launch_capability(self):
        source = (HERE / 'attach.ts').read_text()
        self.assertEqual(source.count("driver.request('POST', '/session'"), 1)
        self.assertIn("debuggerAddress: '127.0.0.1:9222'", source)
        self.assertNotIn("'tauri:options':", source)
        self.assertNotIn('binary:', source)
        self.assertNotIn('driver.create(', source)

    def test_checks_reuse_existing_assertions_without_create(self):
        source = (HERE / 'checks.ts').read_text()
        self.assertIn("../../ui/tauri/native-smoke/assert-unwired.ts", source)
        self.assertIn('keyboard.checks !== 22', source)
        self.assertIn('result.checks !== 3', source)
        self.assertNotIn('driver.create(', source)
        self.assertNotIn("driver.request('POST', '/session'", source)

    def test_source_tree_drift_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            (folder / 'source.rs').write_bytes(b'original')
            prepare.pinned.check_tree(folder, {'source.rs': b'original'})
            (folder / 'source.rs').write_bytes(b'changed')
            with self.assertRaises(prepare.pinned.PatchError):
                prepare.pinned.check_tree(folder, {'source.rs': b'original'})

    def test_diagnostics_never_copy_messages_or_paths(self):
        spec = importlib.util.spec_from_file_location('diagnostics', HERE / 'diagnostics.py')
        diagnostics = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(diagnostics)
        raw = 'C:\\private\\secret\\owner_app.rs(7,2): error CS0123: private secret message'
        self.assertEqual(diagnostics.extract(raw), [{'compiler': 'csharp', 'code': 'CS0123', 'file': 'owner_app.rs', 'line': 7}])
        self.assertEqual(diagnostics.location('/secret/unapproved.rs', 2), {})


if __name__ == '__main__':
    unittest.main()
