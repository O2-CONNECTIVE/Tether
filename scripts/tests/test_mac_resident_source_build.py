"""Synthetic source/compiler fixtures only; real compile is recorded separately."""
import contextlib
import hashlib
import io
import json
import os
import stat
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import build_mac_resident_source as tool


class ResidentSourceBuildTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.parent = Path(self.temporary.name).resolve()
        self.repo = self.parent / 'source'
        self.repo.mkdir()
        self.output = self.parent / 'record'
        self.input = 'backend/internal/residentpipe/codec.go'
        for name, value in {
            self.input: b'package residentpipe\n', 'backend/go.mod': b'module fixture\n',
            'backend/go.sum': b'fixture lock\n',
            tool.RECIPE[0]: b'recipe\n', tool.RECIPE[1]: b'inventory\n',
        }.items():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(value)
        self.git('init', '-q', '--template=')
        self.commit()
        self.real_command = tool.command
        self.calls = []
        self.settings = {'GOVERSION': tool.GO_VERSION, 'GOOS': 'darwin',
                         'GOARCH': 'arm64', 'CGO_ENABLED': '1'}
        self.uncached_all = False
        self.modules = [{'Path': tool.PROJECT, 'Main': True},
                        {'Path': 'example.test/dependency', 'Version': 'v1.0.0',
                         'Sum': 'h1:fixture', 'GoModSum': 'h1:fixture-mod'}]
        self.package = {'ImportPath': tool.ROLE, 'GoFiles': ['codec.go']}
        self.live_files = ['codec.go']
        self.live_extra = {}
        self.external_path = 'example.test/dependency/helper'
        self.archive_bytes = b'!<arch>\nsynthetic compiler archive; never executable'
        self.during_build = lambda stage: None
        self.patches = [patch.object(tool, 'ROOT', self.repo),
                        patch.object(tool, 'product_files_for', return_value={tool.ROLE: {'codec.go'}}),
                        patch.object(tool.platform, 'system', return_value='Darwin'),
                        patch.object(tool.platform, 'machine', return_value='arm64'),
                        patch.object(tool, 'command', side_effect=self.compiler)]
        for item in self.patches:
            item.start()
            self.addCleanup(item.stop)

    def git(self, *args):
        return subprocess.run(['git', '-c', 'core.hooksPath=/dev/null',
                               '-c', 'commit.gpgsign=false', '-c', 'user.name=Fixture',
                               '-c', 'user.email=fixture@example.test', *args],
                              cwd=self.repo, capture_output=True, check=True).stdout

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-q', '-m', 'fixture')

    def compiler(self, args, cwd, env=None):
        if args[0] == 'git':
            return self.real_command(args, cwd, env)
        self.calls.append((args, env))
        self.assertEqual(args[0], 'go')
        if args[1] == 'env':
            return json.dumps(self.settings).encode()
        if args[1:3] == ['mod', 'verify']:
            return b'all modules verified\n'
        if args[1] == 'list' and '-m' in args:
            if self.uncached_all and 'all' in args:
                raise tool.BuildError('BUILD_COMMAND_FAILED')
            return '\n'.join(json.dumps(item) for item in self.modules).encode()
        if args[1] == 'list' and args[-1] == './...':
            return json.dumps({'ImportPath': tool.ROLE, 'GoFiles': self.live_files, **self.live_extra}).encode()
        if args[1] == 'list':
            package = {'Dir': str(Path(cwd) / 'internal/residentpipe'),
                       'Module': self.modules[0] if self.modules else {}, **self.package}
            external = {'ImportPath': self.external_path,
                        'Module': self.modules[1] if len(self.modules) > 1 else {}}
            return (json.dumps(package) + '\n' + json.dumps(external)).encode()
        if args[1] == 'build':
            self.assertIn('-buildmode=archive', args)
            self.assertEqual(args[-1], tool.ROLE)
            self.assertNotIn('run', args)
            self.assertNotIn('test', args)
            Path(args[args.index('-o') + 1]).write_bytes(self.archive_bytes)
            self.during_build(Path(cwd).parent)
            return b''
        self.fail('unexpected build command')

    def rejected(self, code):
        with self.assertRaisesRegex(tool.BuildError, '^' + code + '$'):
            tool.build(self.output)
        self.assertFalse(self.output.exists(), 'failed build left its new output')

    def test_record_binds_git_source_lock_recipe_and_actual_archive(self):
        record = tool.build(self.output)
        self.assertEqual(record['status'], 'blocked')
        self.assertEqual(record['activation'], 'blocked')
        self.assertEqual(record['distribution'], 'blocked')
        self.assertFalse(record['producerExecutable'])
        self.assertFalse(record['cryptographicAttestation'])
        self.assertEqual(record['sourceCommit'], self.git('rev-parse', 'HEAD').decode().strip())
        self.assertEqual(record['snapshotGoFiles'][self.input], hashlib.sha256(b'package residentpipe\n').hexdigest())
        self.assertEqual(record['locks']['backend/go.sum'], hashlib.sha256(b'fixture lock\n').hexdigest())
        self.assertEqual(set(record['recipe']), set(tool.RECIPE))
        self.assertEqual(record['archive']['sha256'], hashlib.sha256(self.archive_bytes).hexdigest())
        self.assertEqual(record['archive']['kind'], 'go-library-archive')
        self.assertEqual(set(record['blockers']), set(tool.BLOCKERS))
        self.assertEqual(json.loads((self.output / 'BUILD_RECORD.json').read_text()), record)
        self.assertNotIn(str(self.parent), json.dumps(record))
        self.assertFalse(list(self.output.glob('.compile-*')))
        self.assertFalse((self.output / record['archive']['path']).stat().st_mode & 0o111)

    def test_cli_valid_build_is_still_exit_two_and_blocked(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            result = tool.main(['--output', str(self.output)])
        self.assertEqual(result, 2)
        self.assertEqual(json.loads(out.getvalue())['code'], 'LIBRARY_BUILD_RECORDED')
        self.assertNotIn(str(self.output), out.getvalue())

    def test_dirty_source_is_rejected_before_compiler_or_output(self):
        (self.repo / self.input).write_text('dirty')
        self.rejected('SOURCE_DIRTY')
        self.assertEqual(self.calls, [])

    def test_untracked_source_is_rejected(self):
        (self.repo / 'backend/unreviewed.go').write_text('package fixture')
        self.rejected('SOURCE_DIRTY')

    def test_committed_extra_production_file_is_not_silently_omitted(self):
        (self.repo / 'backend/internal/residentpipe/injected.go').write_text('package residentpipe\n')
        self.commit()  # A clean HEAD, not an untracked/dirty-source refusal.
        self.live_files.append('injected.go')
        self.rejected('INVENTORY_INVALID')

    def test_committed_native_input_is_not_silently_omitted(self):
        (self.repo / 'backend/internal/residentpipe/extra.h').write_text('fixture header')
        self.commit()
        self.live_extra['HFiles'] = ['extra.h']
        self.rejected('EXTRA_BUILD_INPUT')

    def test_snapshot_uses_git_blob_not_changed_live_file(self):
        revision = self.git('rev-parse', 'HEAD').decode().strip()
        (self.repo / self.input).write_text('different live bytes')
        stage = self.parent / 'snapshot'
        stage.mkdir()
        tool.snapshot(revision, stage)
        self.assertEqual((stage / self.input).read_bytes(), b'package residentpipe\n')

    def test_git_source_symlink_is_rejected_without_following(self):
        # Git's symlink mode is enough to reject before materializing/reading
        # a link. This needs no Windows symlink privilege or platform skip.
        blob = self.git('hash-object', '-w', self.input)
        self.git('update-index', '--cacheinfo', '120000,' + blob.decode().strip() + ',' + self.input)
        self.git('commit', '-q', '-m', 'symbolic fixture mode')
        self.git('update-index', '--assume-unchanged', self.input)
        self.rejected('SOURCE_FILE_INVALID')
        self.assertEqual(self.calls, [])

    def test_recipe_mismatch_cannot_hide_behind_clean_status_flags(self):
        self.git('update-index', '--assume-unchanged', tool.RECIPE[0])
        (self.repo / tool.RECIPE[0]).write_text('different recipe')
        self.rejected('RECIPE_CHANGED')

    def test_source_commit_changed_during_build_is_rejected(self):
        def change(stage):
            (self.repo / self.input).write_text('package residentpipe\n// changed\n')
            self.commit()
        self.during_build = change
        self.rejected('SOURCE_CHANGED')

    def test_snapshot_modified_by_compiler_is_rejected(self):
        self.during_build = lambda stage: (stage / self.input).write_text('changed input')
        self.rejected('BUILD_INPUT_CHANGED')

    def test_toolchain_target_and_cgo_are_exact(self):
        for key, value in [('GOVERSION', 'go0.0'), ('GOOS', 'linux'),
                           ('GOARCH', 'amd64'), ('CGO_ENABLED', '0')]:
            with self.subTest(key=key), patch.dict(self.settings, {key: value}):
                self.rejected('TOOLCHAIN_MISMATCH')

    def test_metadata_stays_with_actual_library_modules_in_offline_cache(self):
        self.uncached_all = True
        record = tool.build(self.output)
        self.assertEqual(record['status'], 'blocked')
        queries = [args for args, _ in self.calls if '-m' in args]
        self.assertEqual(len(queries), 1)
        self.assertNotIn('all', queries[0])
        self.assertEqual(set(queries[0][5:]), {tool.PROJECT, 'example.test/dependency'})

    def test_module_root_package_is_part_of_the_closed_dependency_set(self):
        self.external_path = 'example.test/dependency'
        record = tool.build(self.output)
        self.assertEqual(record['modules'][0]['Path'], self.external_path)

    def test_module_replacement_is_rejected(self):
        self.modules[1]['Replace'] = {'Dir': 'PRIVATE_ARGUMENT'}
        self.rejected('MODULE_REPLACEMENT')

    def test_missing_module_checksums_are_rejected(self):
        del self.modules[1]['GoModSum']
        self.rejected('MODULE_INVALID')

    def test_missing_or_duplicate_main_module_is_rejected(self):
        self.modules = self.modules[1:]
        self.rejected('MODULE_INVALID')
        self.modules = [{'Path': tool.PROJECT, 'Main': True}] * 2
        self.rejected('MODULE_INVALID')

    def test_main_module_identity_is_exact(self):
        self.modules[0]['Path'] = 'other.example.test/source'
        self.rejected('MODULE_INVALID')

    def test_unknown_or_missing_compiled_go_file_is_rejected(self):
        for names in [[], ['codec.go', 'injected.go']]:
            with self.subTest(names=names), patch.dict(self.package, {'GoFiles': names}):
                self.rejected('INVENTORY_INVALID')

    def test_extra_native_or_embed_input_is_rejected(self):
        for field in ['CFiles', 'HFiles', 'SysoFiles', 'EmbedFiles']:
            with self.subTest(field=field), patch.dict(self.package, {field: ['unreviewed']}):
                self.rejected('EXTRA_BUILD_INPUT')

    def test_compiled_source_directory_cannot_escape_snapshot(self):
        self.package['Dir'] = str(self.parent)
        self.rejected('SOURCE_ROOT_INVALID')

    def test_unknown_repository_package_is_rejected(self):
        self.package['ImportPath'] = tool.PROJECT + '/injected'
        self.rejected('INVENTORY_INVALID')

    def test_non_archive_output_is_rejected(self):
        self.archive_bytes = b'not a library archive'
        self.rejected('ARCHIVE_INVALID')

    def test_archive_symlink_and_size_are_rejected(self):
        path = self.parent / 'archive'
        with patch.object(tool.Path, 'lstat', return_value=SimpleNamespace(st_mode=stat.S_IFLNK, st_size=0)), \
                patch.object(tool.Path, 'open', side_effect=AssertionError('symlink followed')), \
                self.assertRaisesRegex(tool.BuildError, 'ARCHIVE_INVALID'):
            tool.archive(path)
        path.write_bytes(b'!<arch>\n' + b'x' * 40)
        with patch.object(tool, 'LIMIT', 32), self.assertRaisesRegex(tool.BuildError, 'ARCHIVE_INVALID'):
            tool.archive(path)

    def test_existing_output_is_never_overwritten(self):
        self.output.mkdir()
        marker = self.output / 'keep'
        marker.write_bytes(b'original')
        with self.assertRaises(FileExistsError):
            tool.build(self.output)
        self.assertEqual(marker.read_bytes(), b'original')

    def test_output_inside_checkout_is_rejected(self):
        with self.assertRaisesRegex(tool.BuildError, 'OUTPUT_INSIDE_SOURCE'):
            tool.build(self.repo / 'output')

    def test_symlinked_output_parent_is_rejected(self):
        link = self.parent / 'link'
        original = tool.Path.resolve
        def resolve(path, *args, **kwargs):
            return self.parent if path == link else original(path, *args, **kwargs)
        with patch.object(tool.Path, 'resolve', new=resolve), \
                self.assertRaisesRegex(tool.BuildError, 'OUTPUT_PARENT_INVALID'):
            tool.build(link / 'record')

    def test_unsupported_host_is_rejected_before_output(self):
        with patch.object(tool.platform, 'system', return_value='Linux'):
            self.rejected('BUILD_HOST_UNSUPPORTED')

    def test_build_environment_is_offline_and_does_not_accept_go_overrides(self):
        with patch.dict(os.environ, {'GOFLAGS': '-toolexec=PRIVATE_ARGUMENT',
                                    'GOWORK': 'PRIVATE_ARGUMENT', 'CGO_LDFLAGS': 'PRIVATE_ARGUMENT'}):
            tool.build(self.output)
        for _, env in self.calls:
            self.assertEqual(env['GOTOOLCHAIN'], 'local')
            self.assertEqual(env['GOPROXY'], 'off')
            self.assertEqual(env['GOWORK'], 'off')
            self.assertEqual(env['GOFLAGS'], '')
            self.assertNotIn('CGO_LDFLAGS', env)

    def test_cli_argument_diagnostics_do_not_echo_input(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            result = tool.main(['--producer', 'PRIVATE_ARGUMENT'])
        self.assertEqual(result, 1)
        self.assertEqual(json.loads(out.getvalue())['code'], 'ARGUMENT_INVALID')
        self.assertNotIn('PRIVATE_ARGUMENT', out.getvalue())


class BuildProcessTests(unittest.TestCase):
    def process(self, returncode=0):
        manager = MagicMock()
        process = manager.__enter__.return_value
        process.returncode = returncode
        return manager, process

    def test_interrupt_waits_same_compiler_and_never_records_success(self):
        manager, process = self.process()
        process.communicate.side_effect = [KeyboardInterrupt(), (b'bytes', b'')]
        with patch.object(tool.subprocess, 'Popen', return_value=manager) as start:
            with self.assertRaisesRegex(tool.BuildError, 'BUILD_INTERRUPTED'):
                tool.command(['go', 'build'], '.')
        self.assertEqual(start.call_count, 1)
        self.assertEqual(process.communicate.call_count, 2)
        process.kill.assert_not_called()
        process.terminate.assert_not_called()
        manager.__exit__.assert_called_once()

    def test_failed_compiler_has_one_owner_and_fixed_error(self):
        manager, process = self.process(returncode=1)
        process.communicate.return_value = (b'', b'PRIVATE_ARGUMENT')
        with patch.object(tool.subprocess, 'Popen', return_value=manager) as start:
            with self.assertRaisesRegex(tool.BuildError, '^BUILD_COMMAND_FAILED$'):
                tool.command(['go', 'build'], '.')
        self.assertEqual(start.call_count, 1)
        manager.__exit__.assert_called_once()


if __name__ == '__main__':
    unittest.main()
