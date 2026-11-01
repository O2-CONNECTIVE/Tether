"""Synthetic Mach-O bytes only; never execute/build/distribute a native binary."""
import importlib.util
import json
import os
from pathlib import Path
import stat
import struct
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import zipfile

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('mac_package', ROOT / 'scripts/package_mac_agent.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def image(cpu=0x0100000c, commands=None, count=1):
    if commands is None:
        commands=struct.pack('<8I',0x32,32,1,26<<16,(26<<16)|(5<<8),1,3,1267<<16)
    return struct.pack('<8I',0xfeedfacf,cpu,0,2,count,len(commands),0,0)+commands


class MacPackageTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name).resolve()
        self.binary = self.root / 'agent'
        self.binary.write_bytes(image())
        self.notices = self.root / 'notices'
        texts = json.loads((ROOT / 'scripts/tests/fixtures/go-licenses/texts.json').read_text())
        for name in m.TEXTS:
            p = self.notices / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(texts[p.name].encode())
        self.output = self.root / 'candidate.zip'

    def args(self):
        return dict(binary=self.binary, expected_sha256=m.sha(self.binary.read_bytes()),
                    target='darwin-arm64', source_manifest_sha256='a'*64,
                    notices_root=self.notices, output=self.output)

    def test_archive_is_deterministic_blocked_and_exact(self):
        # A private unrelated file is never enumerated/copied from notices root.
        (self.notices / 'PRIVATE_CONFIG').write_text('never include')
        result = m.package(**self.args())
        data = self.output.read_bytes()
        self.assertEqual(result['distributionStatus'], 'blocked')
        self.assertEqual(result['archiveSha256'], m.sha(data))
        with zipfile.ZipFile(self.output) as z:
            self.assertEqual(len(z.namelist()), 11)
            self.assertEqual(z.read('rescue-mac-agent'), self.binary.read_bytes())
            manifest = json.loads(z.read('MANIFEST.json'))
            self.assertFalse(manifest['binaryDependenciesVerified'])
            self.assertFalse(manifest['binarySecretAuditVerified'])
            self.assertEqual(manifest['platformDeclaration'], {
                'minimumMacOSDeclared': '26.0.0', 'sdkDeclared': '26.5.0',
                'buildToolsDeclared': {'3': '1267.0.0'}, 'compatibilityVerified': False})
            for name, digest in manifest['files'].items():
                self.assertEqual(m.sha(z.read(name)), digest)
            self.assertEqual((z.getinfo('rescue-mac-agent').external_attr >> 16) & 0o777, 0o555)
            self.assertNotIn(b'never include', data)
        a = self.args(); a['output'] = self.root / 'second.zip'
        m.package(**a)
        self.assertEqual(data, a['output'].read_bytes())

    def test_wrong_hash_target_header_size_and_provenance(self):
        for field, value in [('expected_sha256', '0'*64), ('expected_sha256', 'bad'),
                             ('target', 'darwin-amd64'), ('target', '../../bad'),
                             ('source_manifest_sha256', 'bad')]:
            with self.subTest(field=field, value=value):
                a=self.args(); a[field]=value
                with self.assertRaises(m.PackageError): m.package(**a)
                self.assertFalse(self.output.exists())
        for blob in [b'', b'not MachO', struct.pack('<8I',0xcafebabe,0x0100000c,0,2,0,0,0,0),
                     struct.pack('<8I',0xfeedfacf,0x0100000c,0,6,0,0,0,0),
                     struct.pack('<8I',0xfeedfacf,0x0100000c,0,2,2,100,0,0)]:
            self.binary.write_bytes(blob)
            with self.assertRaises(m.PackageError):m.package(**self.args())

    def test_existing_output_never_overwritten(self):
        self.output.write_bytes(b'keep')
        with self.assertRaises(m.PackageError):m.package(**self.args())
        self.assertEqual(self.output.read_bytes(), b'keep')

    def test_symlink_file_parent_and_output_refused(self):
        link=self.root/'link'; link.symlink_to(self.binary)
        a=self.args();a['binary']=link
        with self.assertRaises(m.PackageError):m.package(**a)
        directory=self.root/'linked-notices';directory.symlink_to(self.notices, target_is_directory=True)
        a=self.args();a['notices_root']=directory
        with self.assertRaises(m.PackageError):m.package(**a)
        self.output.symlink_to(self.root/'missing')
        with self.assertRaises(m.PackageError):m.package(**self.args())
        self.assertFalse((self.root/'missing').exists())

    def test_notice_tamper_missing_and_nonregular(self):
        p=self.notices/next(iter(m.TEXTS));p.write_bytes(b'PRIVATE changed')
        with self.assertRaises(m.PackageError):m.package(**self.args())
        p.unlink()
        with self.assertRaises(OSError):m.package(**self.args())
        p.mkdir()
        with self.assertRaises(m.PackageError):m.package(**self.args())

    def test_oversize_does_not_open(self):
        args = self.args()
        with mock.patch.object(m, 'BINARY_LIMIT', 16), mock.patch.object(m.os, 'open') as opened:
            with self.assertRaisesRegex(m.PackageError, 'SIZE_INVALID'):
                m.package(**args)
            opened.assert_not_called()

    def test_fifo_metadata_rejected_before_open_on_all_platforms(self):
        fifo = mock.Mock(st_mode=stat.S_IFIFO | 0o600, st_file_attributes=0)
        with mock.patch.object(m, 'directory_chain'), mock.patch.object(Path, 'lstat', return_value=fifo), mock.patch.object(m.os, 'open') as opened:
            with self.assertRaisesRegex(m.PackageError, 'FILE_INVALID'):
                m.read_regular(self.binary, 100)
            opened.assert_not_called()

    def test_opened_fifo_metadata_rejected_before_read_on_all_platforms(self):
        fifo = mock.Mock(st_mode=stat.S_IFIFO | 0o600)
        stream = mock.MagicMock()
        stream.__enter__.return_value = stream
        stream.fileno.return_value = 123
        with mock.patch.object(m.os, 'open', return_value=123), mock.patch.object(m.os, 'fdopen', return_value=stream), mock.patch.object(m.os, 'fstat', return_value=fifo):
            with self.assertRaisesRegex(m.PackageError, 'FILE_CHANGED'):
                m.read_regular(self.binary, 100)
            stream.read.assert_not_called()
        stream.__exit__.assert_called_once()

    @unittest.skipUnless(hasattr(os, 'mkfifo'), 'POSIX FIFO creation unavailable; metadata refusal is tested on every OS')
    def test_posix_fifo_rejected_before_open(self):
        self.binary.unlink()
        os.mkfifo(self.binary)
        with mock.patch.object(m.os, 'open') as opened:
            with self.assertRaisesRegex(m.PackageError, 'FILE_INVALID'):
                m.read_regular(self.binary, 100)
            opened.assert_not_called()

    def test_same_verified_bytes_archived_no_reread(self):
        original=m.read_regular
        count=0
        def read(path,limit):
            nonlocal count
            value=original(path,limit)
            if Path(path)==self.binary:
                count+=1;self.binary.write_bytes(b'changed after read')
            return value
        a=self.args()
        with mock.patch.object(m,'read_regular',side_effect=read):m.package(**a)
        self.assertEqual(count,1)
        with zipfile.ZipFile(self.output) as z:self.assertEqual(m.sha(z.read('rescue-mac-agent')),a['expected_sha256'])

    def test_cli_success_amd64(self):
        self.binary.write_bytes(image(0x01000007))
        args=self.args();args['target']='darwin-amd64'
        command=[sys.executable,str(ROOT/'scripts/package_mac_agent.py')]
        for name,value in args.items():command.extend(['--'+name.replace('_','-'),str(value)])
        run=subprocess.run(command,capture_output=True,text=True)
        self.assertEqual(run.returncode,0,run.stdout+run.stderr)
        self.assertEqual(json.loads(run.stdout)['distributionStatus'],'blocked')
        with zipfile.ZipFile(self.output) as archive:
            self.assertEqual(json.loads(archive.read('MANIFEST.json'))['target'],'darwin-amd64')

    def test_platform_declarations_and_malformed_commands(self):
        result=m.macho(image(), 'darwin-arm64')
        self.assertEqual(result['minimumMacOSDeclared'],'26.0.0')
        self.assertEqual(result['sdkDeclared'],'26.5.0')
        self.assertFalse(result['compatibilityVerified'])
        valid=image()[32:]
        invalid=[image(commands=b'',count=0), image(commands=valid,count=2),
                 image(commands=valid+b'12345678'), image(commands=valid+valid,count=2),
                 image(commands=struct.pack('<2I',0x24,8)),
                 image(commands=struct.pack('<2I',1,7)),
                 image(commands=struct.pack('<2I',1,0xffffffff)),
                 image(commands=struct.pack('<6I',0x32,24,2,26<<16,26<<16,0)),
                 image(commands=struct.pack('<6I',0x32,24,1,0,26<<16,0)),
                 image(commands=struct.pack('<6I',0x32,24,1,27<<16,26<<16,0)),
                 image(commands=struct.pack('<6I',0x32,24,1,26<<16,26<<16,1)),
                 image(commands=struct.pack('<8I',0x32,32,1,26<<16,26<<16,1,99,1)),
                 image(commands=struct.pack('<10I',0x32,40,1,26<<16,26<<16,2,3,1,3,1))]
        for blob in invalid:
            with self.subTest(blob=blob):
                with self.assertRaises(m.PackageError):m.macho(blob,'darwin-arm64')

    def test_cli_fixed_error_no_input_leak(self):
        run=subprocess.run([sys.executable,str(ROOT/'scripts/package_mac_agent.py'),'--binary','PRIVATE_ARGUMENT'],capture_output=True,text=True)
        self.assertEqual(run.returncode,1)
        self.assertNotIn('PRIVATE_ARGUMENT',run.stdout+run.stderr)
        self.assertEqual(json.loads(run.stdout)['code'],'ARGUMENT_INVALID')


if __name__ == '__main__':unittest.main()
