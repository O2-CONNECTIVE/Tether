"""Transfer boundary regression fixtures; no native runtime or network."""

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import zipfile

SPEC = importlib.util.spec_from_file_location('repro_bundle', Path(__file__).with_name('bundle.py'))
BUNDLE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUNDLE)


class BundleBoundary(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.tools = self.root / 'tools'
        for name, content in {
            'tauri/bin/tauri-driver.exe': b'fixed driver',
            'edge/msedgedriver.exe': b'fixed edge',
            'edge/Driver_Notes/LICENSE': b'license',
            'tauri-driver.crate': b'fixed archive',
            'tauri-driver-2.1.0/src/server.rs': b'fixed source',
            'driver-provenance.json': b'{}',
        }.items():
            target = self.tools / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
        self.app = self.root / 'ordinary-debug.exe'
        self.app.write_bytes(b'fixed app')
        self.license = self.root / 'LICENSE'
        self.license.write_bytes(b'source license')
        self.archive = self.root / 'bundle.zip'
        self.folder = self.root / 'unpacked'

    def pack(self):
        return BUNDLE.pack(self.app, self.tools, self.license, 'a' * 40, self.archive)[0]

    def rewrite(self, transform):
        with zipfile.ZipFile(self.archive) as source:
            entries = {n: source.read(n) for n in source.namelist()}
        entries = transform(entries)
        with zipfile.ZipFile(self.archive, 'w') as target:
            for name, value in entries.items():
                target.writestr(name, value)

    def test_same_binary_roundtrip_and_post_native_outputs(self):
        expected = self.pack()
        manifest = BUNDLE.unpack(self.archive, self.folder, expected)
        self.assertEqual(manifest['profile'], 'debug')
        self.assertEqual(manifest['features'], ['custom-protocol'])
        self.assertEqual((self.folder / BUNDLE.BINARY_PATHS[0]).read_bytes(), b'fixed app')
        log = self.folder / 'tools/driver.stdout.log'
        log.write_bytes(b'not published')
        with self.assertRaises(ValueError):
            BUNDLE.verify(self.folder, expected)
        BUNDLE.verify(self.folder, expected, after_native=True)

    def test_changed_app_cannot_pass_after_native(self):
        expected = self.pack()
        BUNDLE.unpack(self.archive, self.folder, expected)
        (self.folder / BUNDLE.BINARY_PATHS[0]).write_bytes(b'changed!!')
        with self.assertRaises(ValueError):
            BUNDLE.verify(self.folder, expected, after_native=True)

    def test_changed_driver_source_cannot_pass_after_native(self):
        expected = self.pack()
        BUNDLE.unpack(self.archive, self.folder, expected)
        (self.folder / 'tools/tauri-driver-2.1.0/src/server.rs').write_bytes(b'changed')
        with self.assertRaises(ValueError):
            BUNDLE.verify(self.folder, expected, after_native=True)

    def test_unknown_files_are_rejected_even_after_native(self):
        expected = self.pack()
        BUNDLE.unpack(self.archive, self.folder, expected)
        (self.folder / 'tools/unknown.exe').write_bytes(b'extra')
        with self.assertRaises(ValueError):
            BUNDLE.verify(self.folder, expected, after_native=True)

    def test_archive_tampering_is_rejected(self):
        expected = self.pack()
        self.rewrite(lambda d: {**d, BUNDLE.BINARY_PATHS[0]: b'changed!!'})
        with self.assertRaises(ValueError):
            BUNDLE.unpack(self.archive, self.folder, expected)

    def test_path_traversal_is_rejected_before_writing(self):
        expected = self.pack()
        self.rewrite(lambda d: {**d, '../escaped': b'no'})
        with self.assertRaises(ValueError):
            BUNDLE.unpack(self.archive, self.folder, expected)
        self.assertFalse(self.folder.exists())

    def test_wrong_manifest_hash_is_rejected(self):
        self.pack()
        with self.assertRaises(ValueError):
            BUNDLE.unpack(self.archive, self.folder, '0' * 64)

    def test_unapproved_feature_source_or_profile_is_rejected(self):
        self.pack()
        with zipfile.ZipFile(self.archive) as archive:
            original = json.loads(archive.read('BUILD.json'))
        for key, value in [('features', ['custom-protocol', 'startup-diagnostics']),
                           ('sourceCommit', 'b' * 40), ('profile', 'release')]:
            modified = {**original, key: value}
            raw = json.dumps(modified).encode()
            with self.subTest(key=key), self.assertRaises(ValueError):
                BUNDLE.read_manifest(raw, hashlib.sha256(raw).hexdigest())

    def test_duplicate_manifest_fields_are_rejected(self):
        raw = b'{"schema":1,"schema":1}'
        with self.assertRaises(ValueError):
            BUNDLE.read_manifest(raw, hashlib.sha256(raw).hexdigest())

    def test_oversize_payload_is_rejected_without_upload(self):
        with mock.patch.object(BUNDLE, 'ZIP_LIMIT', 8), self.assertRaises(ValueError):
            self.pack()
        self.assertFalse(self.archive.exists())

    def test_existing_output_is_not_overwritten(self):
        self.archive.write_bytes(b'keep')
        with self.assertRaises(ValueError):
            self.pack()
        self.assertEqual(self.archive.read_bytes(), b'keep')


if __name__ == '__main__':
    unittest.main()
