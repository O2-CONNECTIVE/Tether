"""Synthetic archive and inactive template only; never execute a host binary."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import plistlib
import struct
import sys
import tempfile
import unittest
from unittest import mock
import zipfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
import package_mac_standby as m


def image(cpu=0x0100000c):
    command = struct.pack('<6I', 0x32, 24, 1, 26 << 16, 26 << 16, 0)
    return struct.pack('<8I', 0xfeedfacf, cpu, 0, 2, 1, len(command), 0, 0) + command


class StandbyPackageTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name).resolve()
        self.binary = self.root / 'synthetic-image'
        self.binary.write_bytes(image())
        self.notices = self.root / 'notices'
        texts = json.loads((ROOT / 'scripts/tests/fixtures/go-licenses/texts.json').read_text())
        for name in m.TEXTS:
            p = self.notices / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(texts[p.name].encode('utf-8'))
        self.output = self.root / 'fake-candidate.zip'

    def args(self):
        return dict(binary=self.binary, expected_sha256=m.base.sha(self.binary.read_bytes()),
                    target='darwin-arm64', source_manifest_sha256='a' * 64,
                    notices_root=self.notices, output=self.output)

    def test_pinned_template_checkout_and_fixture_notice_bytes(self):
        rule = 'packaging/macos/local.tether.host.standby.evaluation.plist.in -text'
        self.assertIn(rule, (ROOT / '.gitattributes').read_text().splitlines())
        for name, digest in m.TEXTS.items():
            with self.subTest(notice=name):
                self.assertEqual(m.base.sha((self.notices / name).read_bytes()), digest)

    def test_archive_and_template_are_exact_inactive_and_blocked(self):
        result = m.package(**self.args())
        self.assertEqual(result['distributionStatus'], 'blocked')
        with zipfile.ZipFile(self.output) as archive:
            names = {'bin/tether-host-standby', m.TEMPLATE_MEMBER, 'LICENSE', 'RECIPIENT.txt', 'MANIFEST.json'} | {'notices/' + n for n in m.TEXTS}
            self.assertEqual(set(archive.namelist()), names)
            template = plistlib.loads(archive.read(m.TEMPLATE_MEMBER))
            self.assertEqual(template, {'Label': 'local.tether.host.standby.evaluation',
                'ProgramArguments': ['@TETHER_STANDBY_EXECUTABLE@', '--standby'],
                'LimitLoadToSessionType': 'Aqua', 'RunAtLoad': True, 'KeepAlive': False})
            manifest = json.loads(archive.read('MANIFEST.json'))
            self.assertEqual(manifest['distributionStatus'], 'blocked')
            self.assertFalse(manifest['templateRendered'])
            self.assertFalse(manifest['registrationPerformed'])
            self.assertFalse(manifest['binaryProductIdentityVerified'])
            self.assertFalse(manifest['binaryDependenciesVerified'])
            for name, digest in manifest['files'].items():
                self.assertEqual(m.base.sha(archive.read(name)), digest)


    def test_deterministic_permissions_and_no_unrelated_content(self):
        (self.notices / 'PRIVATE_CONFIG').write_text('NEVER_INCLUDE')
        first = m.package(**self.args())
        payload = self.output.read_bytes()
        a = self.args(); a['output'] = self.root / 'second.zip'
        self.assertEqual(m.package(**a)['archiveSha256'], first['archiveSha256'])
        self.assertEqual(a['output'].read_bytes(), payload)
        self.assertNotIn(b'NEVER_INCLUDE', payload)
        self.assertNotIn(str(self.root).encode(), payload)
        with zipfile.ZipFile(self.output) as archive:
            for name in archive.namelist():
                expected = 0o555 if name == 'bin/tether-host-standby' else 0o444
                self.assertEqual((archive.getinfo(name).external_attr >> 16) & 0o777, expected)
            self.assertNotIn('rescue-mac-agent', archive.namelist())

    def test_bad_hash_target_provenance_and_image_refused(self):
        for field, value in [('expected_sha256', '0' * 64), ('expected_sha256', 'bad'),
                             ('target', 'darwin-amd64'), ('target', '../../other'),
                             ('source_manifest_sha256', 'bad')]:
            with self.subTest(field=field, value=value):
                a = self.args(); a[field] = value
                with self.assertRaises(m.base.PackageError): m.package(**a)
                self.assertFalse(self.output.exists())
        for blob in (b'', b'not a MachO', image()[:40], image()[:32] + b'\0' * 24):
            self.binary.write_bytes(blob)
            with self.assertRaises(m.base.PackageError): m.package(**self.args())
            self.assertFalse(self.output.exists())

    def test_existing_and_symlink_outputs_never_overwritten(self):
        self.output.write_bytes(b'keep')
        with self.assertRaises(m.base.PackageError): m.package(**self.args())
        self.assertEqual(self.output.read_bytes(), b'keep')
        self.output.unlink(); self.output.symlink_to(self.root / 'missing')
        with self.assertRaises(m.base.PackageError): m.package(**self.args())
        self.assertFalse((self.root / 'missing').exists())

    def test_symlink_and_nonregular_inputs_refused(self):
        link = self.root / 'link'; link.symlink_to(self.binary)
        a = self.args(); a['binary'] = link
        with self.assertRaises(m.base.PackageError): m.package(**a)
        directory = self.root / 'directory'; directory.mkdir()
        a['binary'] = directory
        with self.assertRaises(m.base.PackageError): m.package(**a)
        parent = self.root / 'linked-notices'; parent.symlink_to(self.notices, target_is_directory=True)
        a = self.args(); a['notices_root'] = parent
        with self.assertRaises(m.base.PackageError): m.package(**a)
        self.assertFalse(self.output.exists())

    def test_notice_tamper_missing_and_nonregular_refused(self):
        p = self.notices / next(iter(m.TEXTS))
        p.write_bytes(b'private-tamper')
        with self.assertRaises(m.base.PackageError): m.package(**self.args())
        p.unlink()
        with self.assertRaises(OSError): m.package(**self.args())
        p.mkdir()
        with self.assertRaises(m.base.PackageError): m.package(**self.args())
        self.assertFalse(self.output.exists())

    def test_oversized_binary_refused_before_open(self):
        a = self.args()
        with mock.patch.object(m.base, 'BINARY_LIMIT', 16), mock.patch.object(m.base.os, 'open') as opened:
            with self.assertRaisesRegex(m.base.PackageError, 'SIZE_INVALID'): m.package(**a)
            opened.assert_not_called()

    def test_template_exact_bytes_refuse_rendering_extra_keys_and_args(self):
        original = m.base.read_regular
        raw = m.TEMPLATE_PATH.read_bytes()
        variants = [raw.replace(b'\n', b'\r\n'),
                    raw.replace(b'@TETHER_STANDBY_EXECUTABLE@', b'/tmp/other'),
                    raw.replace(b'--standby', b'--status-stdout'),
                    raw.replace(b'</dict>', b'<key>UserName</key><string>root</string></dict>'),
                    raw.replace(b'</dict>', b'<key>KeepAlive</key><true/></dict>')]
        for changed in variants:
            with self.subTest(changed=changed[-50:]):
                def read(path, limit):
                    return changed if Path(path) == m.TEMPLATE_PATH else original(path, limit)
                with mock.patch.object(m.base, 'read_regular', side_effect=read):
                    with self.assertRaisesRegex(m.base.PackageError, 'TEMPLATE_CHANGED'): m.package(**self.args())
                self.assertFalse(self.output.exists())

    def test_verified_binary_bytes_are_not_reread(self):
        a = self.args(); original = m.base.read_regular; reads = []
        def read(path, limit):
            value = original(path, limit)
            if Path(path) == self.binary:
                reads.append(path); self.binary.write_bytes(b'changed after verification')
            return value
        with mock.patch.object(m.base, 'read_regular', side_effect=read): m.package(**a)
        self.assertEqual(len(reads), 1)
        with zipfile.ZipFile(self.output) as archive:
            self.assertEqual(m.base.sha(archive.read('bin/tether-host-standby')), a['expected_sha256'])

    def test_cli_amd64_and_refusal_without_raw_paths(self):
        self.binary.write_bytes(image(0x01000007))
        a = self.args(); a['target'] = 'darwin-amd64'
        argv = []
        for name, value in a.items(): argv.extend(['--' + name.replace('_', '-'), str(value)])
        out = io.StringIO()
        with contextlib.redirect_stdout(out): self.assertEqual(m.main(argv), 0)
        self.assertEqual(json.loads(out.getvalue())['distributionStatus'], 'blocked')
        out = io.StringIO()
        with contextlib.redirect_stdout(out): self.assertEqual(m.main(['--binary', 'PRIVATE/PATH']), 1)
        self.assertNotIn('PRIVATE', out.getvalue())

    def test_import_has_no_packaging_or_io_actions(self):
        spec = importlib.util.spec_from_file_location('standby_import_fixture', ROOT / 'scripts/package_mac_standby.py')
        module = importlib.util.module_from_spec(spec)
        with mock.patch.object(m.base, 'package') as old_package, mock.patch.object(m.base, 'read_regular') as read, mock.patch.object(m.os, 'open') as opened:
            spec.loader.exec_module(module)
            old_package.assert_not_called(); read.assert_not_called(); opened.assert_not_called()
