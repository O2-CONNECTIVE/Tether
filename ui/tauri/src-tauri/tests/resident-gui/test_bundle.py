"""Anonymous byte fixtures only: no native program is built or launched."""
import hashlib
import os
import subprocess
import sys
from pathlib import Path
import plistlib
import runpy
import tempfile
import unittest

bundle = runpy.run_path(str(Path(__file__).with_name('bundle.py')))


class BundleContract(unittest.TestCase):
    def fixture(self, root):
        source = root / 'source'
        source.write_bytes(b'anonymous inert fixture bytes')
        return source, hashlib.sha256(source.read_bytes()).hexdigest()

    def test_exact_copy_fixed_plist_and_exclusive_output(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            source, digest = self.fixture(root)
            bundle['create'](source, root, digest)
            target = root / bundle['EXECUTABLE']
            self.assertEqual(target.read_bytes(), source.read_bytes())
            self.assertEqual(target.stat().st_mode & 0o777, 0o755)
            bundle['verify'](source, root, digest)
            with self.assertRaises(FileExistsError):
                bundle['create'](source, root, digest)
            target.write_bytes(b'changed')
            with self.assertRaises(ValueError):
                bundle['verify'](source, root, digest)

    def test_truncated_xml_cli_has_fixed_stdout_and_no_stderr(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            source, digest = self.fixture(root)
            bundle['create'](source, root, digest)
            plist = root / bundle['NAME'] / 'Contents/Info.plist'
            plist.write_bytes(b'<?xml version="1.0" encoding="UTF-8"?><plist><dict>')
            environment = dict(os.environ, CI_ACTUAL_PRODUCER='1', RUNNER_ENVIRONMENT='github-hosted',
                               RUNNER_OS='macOS', RUNNER_ARCH='ARM64', CI_GUI_BINARY_SHA256=digest)
            result = subprocess.run([sys.executable, str(Path(__file__).with_name('bundle.py')),
                                     'verify', str(source), str(root)], env=environment,
                                    capture_output=True, text=True, check=False)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stdout, 'GUI_FIXTURE_EVENT=wrapper-invalid\n')
            self.assertEqual(result.stderr, '')

    def test_source_symlink_wrong_digest_and_changed_plist_fail(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            source, digest = self.fixture(root)
            alias = root / 'alias'
            alias.symlink_to(source)
            with self.assertRaises(ValueError):
                bundle['create'](alias, root, digest)
            with self.assertRaises(ValueError):
                bundle['create'](source, root, '0' * 64)
            bundle['create'](source, root, digest)
            plist = root / bundle['NAME'] / 'Contents/Info.plist'
            plist.write_bytes(plistlib.dumps(dict(bundle['PLIST'], LSUIElement=True)))
            with self.assertRaises(ValueError):
                bundle['verify'](source, root, digest)


if __name__ == '__main__':
    unittest.main()
