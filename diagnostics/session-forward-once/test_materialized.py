"""Actual materialized Python subprocess regression; Rust/native entry is blocked."""
import contextlib
import hashlib
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch
import driver
import overlay
import pins
import test_once

ROOT = Path(__file__).resolve().parents[2]
DEPENDENCIES = {
    'scripts/verify_driver_trace.py': 'f9f02834bc2a831d94c7c893845e193527ca785ab454b67cde0590d549569399',
    'scripts/verify_tauri_evaluation.py': 'bf3dab4d08993921a22ff042217868ec076404177536f06f6a2710008b01c3b8',
    'scripts/package_tauri_evaluation.py': 'c8d084f4c27bf169baf1d4aeacf24e89a22205d031c02db35c9cdf03435b5c6b',
}


class RustBoundaryReached(RuntimeError):
    pass


class MaterializedTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.base = Path(cls.temp.name)
        cls.source = cls.base / 'source'
        test_once.CheckoutTests.clone(Path(os.environ.get('FORWARD_TEST_PRODUCT', str(ROOT))), pins.PRODUCT, cls.source, True)
        test_once.CheckoutTests.clone(Path(os.environ.get('FORWARD_TEST_DIAGNOSTIC', str(ROOT))), pins.DIAGNOSTIC, cls.source / '.diagnostic', True)
        cls.archive = Path(os.environ['SESSION_FORWARD_ARCHIVE']).resolve()
        cls.assert_archive = hashlib.sha256(cls.archive.read_bytes()).hexdigest()
        if cls.assert_archive != '2f2e29bd6900fb718dfdc06f2e7a371d6087c085daaf83709847ef3e6b0c1953':
            raise ValueError('PINNED_ARCHIVE_REQUIRED')

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def scope(self):
        root = self.base / self.id().rsplit('.', 1)[-1]
        overlay.materialize(self.source, self.source / '.diagnostic', root, True)
        for name in ('safe', 'private', 'tools'):
            (root / name).mkdir()
        archive = root / 'tools/tauri-driver.crate'
        shutil.copyfile(self.archive, archive)
        return root, archive

    def python_preflight(self, root, archive):
        real = driver.quiet
        def python_only(folder, label, *args, **kwargs):
            if label == 'real-handler-fixture':
                raise RustBoundaryReached('LOCAL_RUST_NOT_EXECUTED')
            self.assertEqual(label, 'fixed-source-python')
            return real(folder, label, *args, **kwargs)
        # A separate process from an unrelated cwd cannot borrow checkout imports.
        with contextlib.chdir(root / 'private'), patch.dict(os.environ, {
            'PYTHONPATH': '', 'PYTHONNOUSERSITE': '1', 'PYTHONDONTWRITEBYTECODE': '1'}), \
                patch.object(driver, 'quiet', side_effect=python_only):
            driver.preflight(root, archive)

    def test_actual_materialized_python_passes_five_before_rust_boundary(self):
        root, archive = self.scope()
        with self.assertRaises(RustBoundaryReached):
            self.python_preflight(root, archive)
        facts = json.loads((root / 'safe/transport-preflight.json').read_text())
        self.assertEqual((facts['fixedPython'], facts['fixedPythonPassed']), ('passed', 5))
        self.assertEqual(facts['realHandlerAndRecorderPassed'], 0)
        self.assertEqual(facts['nativeTrials'], 0)
        for name, digest in DEPENDENCIES.items():
            canonical = overlay.git(self.source, 'show', pins.PRODUCT + ':' + name, binary=True)
            self.assertEqual(hashlib.sha256(canonical).hexdigest(), digest)
            self.assertEqual((root / 'diagnostic-scope' / name).read_bytes(), canonical)

    def test_original_missing_import_control_is_red_before_rust(self):
        root, archive = self.scope()
        # Synthetic replay of PR232's original 15-file scope, byte-for-byte otherwise.
        for name in DEPENDENCIES:
            file = root / 'diagnostic-scope' / name
            if file.exists():
                file.unlink()
        with self.assertRaises(ValueError):
            overlay.materialize(self.source, self.source / '.diagnostic', root)
        with self.assertRaises(ValueError):
            self.python_preflight(root, archive)
        facts = json.loads((root / 'safe/transport-preflight.json').read_text())
        self.assertEqual(facts['fixedPython'], 'failed')
        self.assertEqual(facts['realHandler'], 'not-run')
        self.assertEqual(facts['nativeTrials'], 0)

    def test_missing_wrong_stale_extra_and_source_drift_fail_closed(self):
        root, _ = self.scope()
        for name in DEPENDENCIES:
            path = root / 'diagnostic-scope' / name
            canonical = overlay.git(self.source, 'show', pins.PRODUCT + ':' + name, binary=True)
            for changed in (None, b'# SYNTHETIC_WRONG_DEPENDENCY\n', canonical + b'\n# SYNTHETIC_STALE\n'):
                if changed is None:
                    path.unlink()
                else:
                    path.write_bytes(changed)
                with self.assertRaises(ValueError):
                    overlay.materialize(self.source, self.source / '.diagnostic', root)
                path.write_bytes(canonical)
        extra = root / 'diagnostic-scope/scripts/unrequested.py'
        extra.write_text('# SYNTHETIC_EXTRA\n')
        with self.assertRaises(ValueError):
            overlay.materialize(self.source, self.source / '.diagnostic', root)
        extra.unlink()
        source = self.source / 'scripts/verify_driver_trace.py'
        original = source.read_bytes()
        try:
            source.write_bytes(original + b'\n# SYNTHETIC_SOURCE_DRIFT\n')
            with self.assertRaises(ValueError):
                overlay.materialize(self.source, self.source / '.diagnostic', root)
        finally:
            source.write_bytes(original)


if __name__ == '__main__':
    unittest.main()
