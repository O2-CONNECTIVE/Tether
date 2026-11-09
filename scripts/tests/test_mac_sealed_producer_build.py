"""Anonymous compiler fixtures; actual compilation is a separate hosted CI result."""
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import stat
import struct
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import build_mac_sealed_producer as tool
import check_mac_sealed_producer_ci as check


def executable():
    return (struct.pack('<8I', 0xfeedfacf, 0x100000c, 0, 2, 1, 24, 0, 0)
            + struct.pack('<6I', 0x32, 24, 1, 27 << 16, 27 << 16, 0))


class SealedProducerBuildTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.output = self.root / 'output'
        self.repo = self.root / 'repo'
        self.repo.mkdir()
        self.calls = []
        self.revision, self.tree = 'a' * 40, 'b' * 40
        self.image = executable()
        self.source_bytes = b'package main\n'
        self.input = 'backend/cmd/resident-pipe/main.go'
        self.during_build = lambda stage: None
        self.settings = {'GOVERSION': 'go1.27.3', 'GOOS': 'darwin', 'GOARCH': 'arm64', 'CGO_ENABLED': '1'}
        def snapshot(revision, stage, recipe):
            self.assertEqual(revision, self.revision)
            self.assertEqual(recipe, tool.RECIPE)
            path = stage / self.input
            path.parent.mkdir(parents=True)
            path.write_bytes(self.source_bytes)
            return {self.input: tool.source.digest(self.source_bytes)}, {}, {p: 'c'*64 for p in recipe}
        self.mocks = {}
        for name, replacement in {
            'ROOT': self.repo, 'head': lambda: (self.revision, self.tree),
            'snapshot': snapshot, 'build_metadata': lambda *a: self.settings,
            'source_inventory': lambda *a: None,
            'closure': lambda m, e, role: ([role], []),
            'module_metadata': lambda *a: [], 'command': self.compiler,
        }.items():
            item = patch.object(tool.source, name, replacement)
            item.start()
            self.addCleanup(item.stop)
        for name, value in [('system', 'Darwin'), ('machine', 'arm64')]:
            item = patch.object(tool.platform, name, return_value=value)
            item.start()
            self.addCleanup(item.stop)

    def compiler(self, args, cwd, env):
        self.calls.append((args, env))
        self.assertEqual(args[:2], ['go', 'build'])
        self.assertEqual(args[-1], tool.ROLE)
        self.assertEqual(args[2:7], list(tool.FLAGS))
        output = Path(args[args.index('-o') + 1])
        output.write_bytes(self.image)
        self.during_build(Path(cwd).parent)
        return b''

    def test_fixed_executable_build_stays_blocked_without_execution_or_signing(self):
        record = tool.build(self.output)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(record['image']['sha256'], tool.source.digest(self.image))
        self.assertEqual(record['target'], 'darwin-arm64')
        self.assertEqual(stat.S_IMODE((self.output / 'resident-pipe').stat().st_mode), 0o600)
        self.assertEqual(set(p.name for p in self.output.iterdir()), {'resident-pipe', 'BUILD_RECORD.json'})
        check.validate_result(2, tool.summary(record), record, self.revision, self.tree)

    def test_environment_cannot_inject_flags_workspace_or_credentials(self):
        with patch.dict(os.environ, {'GOFLAGS': '-tags=evil', 'CGO_CFLAGS': '-D EVIL',
                                     'GOWORK': '/private/work', 'TETHER_TOKEN': 'secret'}):
            tool.build(self.output)
        env = self.calls[0][1]
        self.assertEqual(env['GOFLAGS'], '')
        self.assertEqual(env['GOWORK'], 'off')
        self.assertEqual(env['GOPROXY'], 'off')
        self.assertNotIn('CGO_CFLAGS', env)
        self.assertNotIn('TETHER_TOKEN', env)

    def test_archives_wrong_arch_and_non_executable_images_are_refused(self):
        for data in (b'!<arch>\n', executable().replace(struct.pack('<I', 0x100000c), struct.pack('<I', 0x1000007)),
                     executable()[:12] + struct.pack('<I', 6) + executable()[16:]):
            self.image = data
            with self.subTest(data=data[:12]), self.assertRaises(tool.image.PackageError):
                tool.build(self.output)
            self.assertFalse(self.output.exists())

    def test_source_mutation_during_compilation_is_refused_and_owned_output_removed(self):
        self.during_build = lambda stage: (stage / self.input).write_bytes(b'changed')
        with self.assertRaisesRegex(tool.source.BuildError, 'BUILD_INPUT_CHANGED'):
            tool.build(self.output)
        self.assertFalse(self.output.exists())

    def test_changed_head_is_refused(self):
        with patch.object(tool.source, 'head', side_effect=[(self.revision, self.tree), ('d'*40, self.tree)]):
            with self.assertRaisesRegex(tool.source.BuildError, 'SOURCE_CHANGED'):
                tool.build(self.output)
        self.assertFalse(self.output.exists())

    def test_preexisting_output_is_never_modified(self):
        self.output.mkdir()
        (self.output / 'original').write_bytes(b'keep')
        with self.assertRaises(FileExistsError):
            tool.build(self.output)
        self.assertEqual((self.output / 'original').read_bytes(), b'keep')
        self.assertEqual(self.calls, [])

    def test_symlink_parent_and_checkout_output_are_rejected(self):
        link = self.root / 'alias'
        link.symlink_to(self.repo, target_is_directory=True)
        for path in (link / 'out', self.repo / 'out'):
            with self.subTest(path=path), self.assertRaises(tool.source.BuildError):
                tool.build(path)
        self.assertEqual(self.calls, [])

    def test_intel_or_other_host_is_not_cross_build_authority(self):
        for system, arch in [('Linux', 'arm64'), ('Darwin', 'x86_64')]:
            with patch.object(tool.platform, 'system', return_value=system), patch.object(tool.platform, 'machine', return_value=arch):
                with self.assertRaisesRegex(tool.source.BuildError, 'BUILD_HOST_UNSUPPORTED'):
                    tool.build(self.output)
        self.assertEqual(self.calls, [])

    def test_cli_no_role_flags_source_or_runtime_path_override(self):
        for option in ('--role', '--source', '--config', '--flags', '--target'):
            stream = io.StringIO()
            with contextlib.redirect_stdout(stream):
                self.assertEqual(tool.main([option, '/private/value']), 1)
            self.assertNotIn('/private/value', stream.getvalue())
            self.assertEqual(json.loads(stream.getvalue())['code'], 'ARGUMENT_INVALID')
        self.assertEqual(self.calls, [])

    def test_cli_exit_two_is_build_record_not_activation(self):
        stream = io.StringIO()
        with contextlib.redirect_stdout(stream):
            self.assertEqual(tool.main(['--output', str(self.output)]), 2)
        self.assertNotIn(str(self.root), stream.getvalue())

    def test_false_successes_and_boundary_promotions_are_rejected(self):
        record = tool.build(self.output)
        for status in (0, 1, 3):
            with self.assertRaises(ValueError):
                check.validate_result(status, tool.summary(record), record, self.revision, self.tree)
        for field, value in [('activation', 'enabled'), ('producerExecuted', True), ('signingPerformed', True),
                             ('enrollmentAccessed', True), ('cryptographicAttestation', True),
                             ('producerExecutable', False), ('role', 'other'), ('target', 'darwin-amd64'),
                             ('flags', []), ('compiledRepositoryPackages', []), ('blockers', [])]:
            bad = copy.deepcopy(record)
            bad[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                check.validate_result(2, tool.summary(bad), bad, self.revision, self.tree)


if __name__ == '__main__':
    unittest.main()
