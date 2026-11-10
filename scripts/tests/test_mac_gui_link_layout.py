"""Synthetic Mach-O bytes only; no compiler, native load, signing or network."""
import hashlib
import json
import plistlib
import os
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
TOOL = ROOT / 'scripts/verify_mac_gui_link_layout.py'
NATIVE = '@rpath/libResidentPreparation.dylib'
SWIFT = '@rpath/libswiftCore.dylib'
FRAMEWORK = '/System/Library/Frameworks/Foundation.framework/Versions/C/Foundation'
RPATH = '@executable_path/../Frameworks'


def text_command(command, text, fixed=12):
    payload = text.encode() + b'\0'
    size = (fixed + len(payload) + 7) // 8 * 8
    return (struct.pack('<3I', command, size, fixed) + b'\0' * (fixed - 12)
            + payload + b'\0' * (size - fixed - len(payload)))


def dylib(command, text):
    return text_command(command, text, 24)


def image(role, extra=(), arch=0x100000c, minimum=27 << 16, commands=None):
    if commands is None:
        commands = [struct.pack('<6I', 0x32, 24, 1, minimum, 27 << 16, 0)]
        if role == 'gui':
            commands += [dylib(0xc, NATIVE), text_command(0x8000001c, RPATH)]
        else:
            commands += [dylib(0xd, NATIVE)]
        commands += [dylib(0xc, FRAMEWORK), dylib(0xc, SWIFT), text_command(0x8000001c, '/usr/lib/swift')]
    commands = list(commands) + list(extra)
    body = b''.join(commands)
    return struct.pack('<8I', 0xfeedfacf, arch, 0, 2 if role == 'gui' else 6,
                       len(commands), len(body), 0, 0) + body


class LinkLayoutTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name).resolve() / 'Candidate.app'
        self.paths = {'gui': self.root / 'Contents/MacOS/rescue-host-shell',
                      'native': self.root / 'Contents/Frameworks/libResidentPreparation.dylib'}
        self.hashes = {}
        for role in self.paths:
            self.write(role, image(role))

    def write(self, role, data):
        path = self.paths[role]
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        self.hashes[role] = hashlib.sha256(data).hexdigest()

    def run_tool(self, extra=()):
        return subprocess.run([sys.executable, '-I', str(TOOL), '--app', str(self.root),
            '--target', 'darwin-arm64', '--gui-sha256', self.hashes['gui'],
            '--native-sha256', self.hashes['native'], *extra], capture_output=True, text=True, timeout=15)

    def refused(self, extra=()):
        result = self.run_tool(extra)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertEqual(result.stderr, '')
        output = json.loads(result.stdout)
        self.assertEqual(output['status'], 'refused')
        self.assertNotIn('PRIVATE', result.stdout)
        self.assertNotIn(str(self.root), result.stdout)
        return output

    def test_fixed_layout_stays_blocked_and_reads_only(self):
        before = {p: p.read_bytes() for p in self.paths.values()}
        result = self.run_tool()
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertEqual(result.stderr, '')
        report = json.loads(result.stdout)
        self.assertEqual(report['status'], 'blocked')
        self.assertTrue(report['linkLayoutMatched'])
        self.assertFalse(report['imageLoaded'])
        self.assertFalse(report['runtimeVerified'])
        self.assertTrue(report['productionGateClosed'])
        self.assertEqual(report['imageHashes'], self.hashes)
        self.assertEqual(report['minimumMacOSDeclared'], '27.0.0')
        self.assertEqual(before, {p: p.read_bytes() for p in self.paths.values()})

    def test_each_image_requires_independent_expected_hash(self):
        for role in self.paths:
            with self.subTest(role=role):
                original = self.hashes[role]
                self.hashes[role] = '0' * 64
                self.refused()
                self.hashes[role] = original

    def test_absolute_build_rpath_and_relative_aliases_are_refused(self):
        for role in self.paths:
            for path in ('/private/PRIVATE/build', '.', '@loader_path', RPATH + '/..',
                         '/usr/lib/swift/../PRIVATE', '/usr/lib/swift/', '@executable_path'):
                with self.subTest(role=role, path=path):
                    self.write(role, image(role, [text_command(0x8000001c, path)]))
                    self.refused()
            self.write(role, image(role))

    def test_private_dependencies_and_dyld_environment_are_refused(self):
        for command in (0xc, 0x80000018, 0x8000001f, 0x80000023, 0x20, 0x10):
            self.write('gui', image('gui', [dylib(command, '/PRIVATE/lib.dylib')]))
            self.refused()
        self.write('gui', image('gui', [text_command(0x27, 'DYLD_INSERT_LIBRARIES=/PRIVATE')]))
        self.refused()

    def test_system_path_lookalikes_and_traversal_are_refused(self):
        for path in ('/usr/library/PRIVATE', '/usr/lib/../PRIVATE',
                     '/System/Library/Frameworks/../PRIVATE', '/usr/lib//PRIVATE',
                     '@rpath/PRIVATE.dylib', '@rpath/libswift/PRIVATE.dylib',
                     '/System/Library/PrivateFrameworks/PRIVATE.framework/PRIVATE'):
            self.write('native', image('native', [dylib(0xc, path)]))
            self.refused()

    def test_required_native_link_is_unique_and_not_weak(self):
        self.write('gui', image('gui', [dylib(0xc, NATIVE)]))
        self.refused()
        commands = [struct.pack('<6I', 0x32, 24, 1, 27 << 16, 27 << 16, 0),
                    dylib(0x80000018, NATIVE), text_command(0x8000001c, RPATH)]
        self.write('gui', image('gui', commands=commands))
        self.refused()
        self.write('gui', image('gui', commands=commands[:1]))
        self.refused()

    def test_native_identity_and_self_dependency_are_refused(self):
        for command in (dylib(0xd, NATIVE), dylib(0xc, NATIVE),
                        dylib(0xd, '/PRIVATE/libResidentPreparation.dylib')):
            self.write('native', image('native', [command]))
            self.refused()
        self.write('native', image('native', commands=[struct.pack('<6I', 0x32, 24, 1, 27 << 16, 27 << 16, 0)]))
        self.refused()

    def test_missing_duplicate_rpath_and_unresolved_swift_are_refused(self):
        self.write('gui', image('gui', [text_command(0x8000001c, RPATH)]))
        self.refused()
        commands = [struct.pack('<6I', 0x32, 24, 1, 27 << 16, 27 << 16, 0), dylib(0xc, NATIVE)]
        self.write('gui', image('gui', commands=commands))
        self.refused()
        self.write('gui', image('gui', commands=commands + [text_command(0x8000001c, RPATH), dylib(0xc, SWIFT)]))
        self.refused()

    def test_framework_directory_cannot_shadow_system_swift(self):
        extra = self.paths['native'].parent / 'libswiftCore.dylib'
        extra.write_bytes(b'PRIVATE')
        self.refused()

    def test_framework_subdirectories_are_refused(self):
        (self.paths['native'].parent / 'PRIVATE').mkdir()
        self.refused()

    def test_missing_files_and_nonregular_images_are_refused(self):
        for role in self.paths:
            path = self.paths[role]
            path.unlink()
            self.refused()
            path.mkdir()
            self.refused()
            path.rmdir()
            self.write(role, image(role))

    @unittest.skipUnless(os.name == 'posix', 'symlink fixture requires POSIX')
    def test_symlink_image_or_parent_is_refused(self):
        native = self.paths['native']
        real = native.with_name('real')
        native.rename(real)
        native.symlink_to(real)
        self.refused()
        native.unlink()
        real.rename(native)
        directory = native.parent
        alternate = directory.with_name('PRIVATE')
        directory.rename(alternate)
        directory.symlink_to(alternate, target_is_directory=True)
        self.refused()

    def test_architecture_kind_and_platform_declarations_are_checked(self):
        for role in self.paths:
            for offset, value in ((0, 0xcafebabe), (4, 0x1000007), (12, 1), (28, 1)):
                data = bytearray(image(role))
                struct.pack_into('<I', data, offset, value)
                self.write(role, data)
                self.refused()
            self.write(role, image(role))
        self.write('native', image('native', minimum=28 << 16))
        self.refused()

    def test_native_minimum_is_combined_not_hidden_by_gui(self):
        self.write('gui', image('gui', minimum=13 << 16))
        result = self.run_tool()
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertEqual(json.loads(result.stdout)['minimumMacOSDeclared'], '27.0.0')

    def test_malformed_command_and_string_bounds_are_refused(self):
        for command in (struct.pack('<2I', 0xc, 8), struct.pack('<3I', 0x8000001c, 12, 12),
                        struct.pack('<4I', 0x8000001c, 16, 8, 0),
                        struct.pack('<4I', 0x8000001c, 16, 12, 0x41414141),
                        text_command(0x8000001c, RPATH)[:-1],
                        text_command(0x8000001c, RPATH)[:-1] + b'X'):
            self.write('gui', image('gui', [command]))
            self.refused()
        for data in (b'', b'PRIVATE', image('gui')[:40]):
            self.write('gui', data)
            self.refused()

    def test_unrecognized_load_commands_refused(self):
        self.write('gui', image('gui', [struct.pack('<2I', 0x999, 8)]))
        self.refused()

    def test_expected_argument_grammar_is_closed(self):
        for extra in (('--target', 'darwin-PRIVATE'), ('--native-sha256', 'PRIVATE'),
                      ('--gui-sha256', 'A'*64), ('--execute',)):
            self.refused(extra)

    def test_image_size_limit_is_bounded(self):
        with self.paths['gui'].open('wb') as output:
            output.truncate(256 * 1024 * 1024 + 1)
        self.refused()

    def test_weak_system_framework_allowed_but_not_proof_of_availability(self):
        self.write('native', image('native', [dylib(0x80000018,
            '/System/Library/Frameworks/LightweightCodeRequirements.framework/Versions/A/LightweightCodeRequirements')]))
        result = self.run_tool()
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertFalse(json.loads(result.stdout)['runtimeVerified'])


class ThreeImageAssemblyTests(unittest.TestCase):
    def setUp(self):
        sys.path.insert(0, str(ROOT / 'scripts'))
        import verify_mac_gui_link_layout as layout
        self.tool = layout
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.app = Path(temporary.name).resolve() / 'Tether.app'
        self.hashes = {}
        for role in layout.ASSEMBLY_IMAGES:
            self.write(role, image(role) if role != 'producer' else self.producer())
        self.plist = self.app / 'Contents/Info.plist'
        self.plist.write_bytes(plistlib.dumps({'CFBundleExecutable': 'rescue-host-shell',
            'CFBundleIdentifier': 'com.o2connective.rescue-host.shell', 'CFBundlePackageType': 'APPL',
            'LSMinimumSystemVersion': '27.0.0'}))

    def producer(self, extra=()):
        commands = [struct.pack('<6I', 0x32, 24, 1, 27 << 16, 27 << 16, 0),
                    text_command(0xe, '/usr/lib/dyld'), dylib(0xc, '/usr/lib/libSystem.B.dylib')]
        return image('gui', extra, commands=commands)

    def write(self, role, data):
        path = self.app / self.tool.ASSEMBLY_IMAGES[role]
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        self.hashes[role] = hashlib.sha256(data).hexdigest()

    def verify(self):
        return self.tool.verify_assembly(self.app, 'darwin-arm64', self.hashes)

    def test_all_three_fixed_images_are_required_and_stay_blocked(self):
        record = self.verify()
        self.assertEqual(record['imageHashes'], self.hashes)
        self.assertEqual(record['status'], 'blocked')
        self.assertTrue(record['producerIncluded'])
        self.assertTrue(record['productionGateClosed'])
        self.assertFalse(record['imageLoaded'])
        self.assertFalse(record['runtimeVerified'])
        del self.hashes['producer']
        with self.assertRaises(self.tool.LayoutError):
            self.verify()

    def test_producer_hash_role_architecture_and_private_loader_inputs_are_refused(self):
        original = self.hashes['producer']
        self.hashes['producer'] = '0' * 64
        with self.assertRaises(self.tool.LayoutError):
            self.verify()
        self.hashes['producer'] = original
        wrong_arch = bytearray(self.producer())
        struct.pack_into('<I', wrong_arch, 4, 0x1000007)
        for data in (image('native'), wrong_arch,
                     self.producer([text_command(0x8000001c, '/PRIVATE')]),
                     self.producer([text_command(0x8000001c, RPATH)]),
                     self.producer([dylib(0xc, NATIVE)]),
                     self.producer([dylib(0xc, SWIFT)]),
                     self.producer([dylib(0xc, '/PRIVATE/lib.dylib')]),
                     self.producer([text_command(0x27, 'DYLD_INSERT_LIBRARIES=/PRIVATE')])):
            self.write('producer', data)
            with self.subTest(data=data[:40]), self.assertRaises(self.tool.LayoutError):
                self.verify()

    def test_extra_file_directory_missing_producer_or_wrong_plist_is_refused(self):
        for relative in ('extra', 'Contents/extra', 'Contents/MacOS/extra', 'Contents/Helpers/extra'):
            path = self.app / relative
            for directory in (False, True):
                path.mkdir() if directory else path.write_bytes(b'extra')
                with self.subTest(path=relative, directory=directory), self.assertRaises(self.tool.LayoutError):
                    self.verify()
                path.rmdir() if directory else path.unlink()
        plist = plistlib.loads(self.plist.read_bytes())
        for key in ('CFBundleExecutable', 'CFBundleIdentifier', 'CFBundlePackageType', 'LSMinimumSystemVersion'):
            bad = dict(plist, **{key: 'other'})
            self.plist.write_bytes(plistlib.dumps(bad))
            with self.subTest(key=key), self.assertRaises(self.tool.LayoutError):
                self.verify()
        self.plist.write_bytes(plistlib.dumps(plist))
        (self.app / self.tool.ASSEMBLY_IMAGES['producer']).unlink()
        with self.assertRaises(self.tool.LayoutError):
            self.verify()

    @unittest.skipUnless(os.name == 'posix', 'symlink fixture requires POSIX')
    def test_producer_and_helper_directory_symlinks_are_refused(self):
        path = self.app / self.tool.ASSEMBLY_IMAGES['producer']
        data = path.read_bytes()
        outside = self.app.parent / 'outside'
        outside.write_bytes(data)
        path.unlink()
        path.symlink_to(outside)
        with self.assertRaises(self.tool.files.PackageError):
            self.verify()
        path.unlink()
        path.write_bytes(data)
        helpers = path.parent
        moved = self.app.parent / 'moved'
        helpers.rename(moved)
        helpers.symlink_to(moved, target_is_directory=True)
        with self.assertRaises(self.tool.files.PackageError):
            self.verify()


if __name__ == '__main__':
    unittest.main()
