"""Anonymous compiler products; real compilation is checked separately on hosted Mac CI."""
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import build_mac_gui_candidate as tool
import check_mac_gui_candidate_ci as check
from scripts.tests.test_mac_gui_link_layout import image


class GuiCandidateBuildTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.output = self.root / 'output'
        self.repo = self.root / 'repo'
        self.repo.mkdir()
        self.revision, self.tree = 'a' * 40, 'b' * 40
        self.calls = []
        self.source_bytes = b'fixed source\n'
        self.input = 'ui/tauri/source.txt'
        self.images = {role: image(role) for role in ('gui', 'native')}
        self.mutate = lambda stage: None
        self.patch(tool.source, 'ROOT', self.repo)
        self.patch(tool.source, 'head', lambda: (self.revision, self.tree))
        self.patch(tool.support, 'snapshot', self.snapshot)
        self.patch(tool.support, 'toolchain', lambda *a: ('/swift', '/sdk', {'deploymentTarget': '14.4'}))
        self.patch(tool.support, 'compile_images', self.compiler)
        self.patch(tool.platform, 'system', lambda: 'Darwin')
        self.patch(tool.platform, 'machine', lambda: 'arm64')
        env = patch.dict(os.environ, {'RUNNER_ENVIRONMENT': 'github-hosted'})
        env.start()
        self.addCleanup(env.stop)

    def patch(self, owner, name, value):
        mocked = patch.object(owner, name, value)
        mocked.start()
        self.addCleanup(mocked.stop)

    def snapshot(self, revision, stage):
        self.assertEqual(revision, self.revision)
        path = stage / self.input
        path.parent.mkdir(parents=True)
        path.write_bytes(self.source_bytes)
        config = stage / 'ui/tauri/src-tauri/tauri.conf.json'
        config.parent.mkdir()
        config.write_text(json.dumps({'identifier': 'com.o2connective.rescue-host.shell',
                                     'version': '0.1.0', 'bundle': {'active': False}}))
        return {self.input: tool.source.digest(self.source_bytes)}, {p: 'c' * 64 for p in tool.support.RECIPE}

    def compiler(self, stage, env, swift, sdk):
        self.calls.append(env)
        products = {}
        for role, data in self.images.items():
            products[role] = stage / role
            products[role].write_bytes(data)
        self.mutate(stage)
        return products

    def test_public_cli_assembles_inert_images_and_retains_blocked_record(self):
        stream = io.StringIO()
        with contextlib.redirect_stdout(stream):
            self.assertEqual(tool.main(['--output', str(self.output)]), 2)
        record = json.loads((self.output / 'BUILD_RECORD.json').read_text())
        self.assertEqual(json.loads(stream.getvalue()), tool.summary(record))
        self.assertNotIn(str(self.root), stream.getvalue())
        self.assertEqual(len(self.calls), 1)
        check.validate(record, self.revision, self.tree)
        app = self.output / tool.support.APP
        self.assertEqual({p.relative_to(app).as_posix() for p in app.rglob('*') if p.is_file()},
                         {'Contents/Info.plist', *tool.layout.IMAGES.values()})
        for role, relative in tool.layout.IMAGES.items():
            self.assertEqual((app / relative).read_bytes(), self.images[role])

    @unittest.skipUnless(os.name == 'posix', 'POSIX permission bits required')
    def test_images_are_nonexecutable_and_output_is_private(self):
        tool.build(self.output)
        self.assertEqual(stat.S_IMODE(self.output.stat().st_mode), 0o700)
        for path in tool.layout.IMAGES.values():
            self.assertEqual(stat.S_IMODE((self.output / tool.support.APP / path).stat().st_mode), 0o600)

    def test_environment_cannot_inject_fixture_flags_or_credentials(self):
        with patch.dict(os.environ, {'RUSTFLAGS': '--cfg resident_gui_fixture',
             'CARGO_ENCODED_RUSTFLAGS': '--cfg resident_launch_fixture',
             'SWIFTFLAGS': '-DRESIDENT_LAUNCH_FIXTURE', 'TETHER_TOKEN': 'PRIVATE',
             'NODE_OPTIONS': '--require /PRIVATE', 'npm_config_registry': 'https://PRIVATE',
             'CARGO_BUILD_RUSTFLAGS': 'PRIVATE', 'DYLD_INSERT_LIBRARIES': '/PRIVATE'}):
            tool.build(self.output)
        env = self.calls[0]
        for key in ('RUSTFLAGS', 'CARGO_ENCODED_RUSTFLAGS', 'SWIFTFLAGS', 'TETHER_TOKEN',
                    'NODE_OPTIONS', 'npm_config_registry', 'CARGO_BUILD_RUSTFLAGS', 'DYLD_INSERT_LIBRARIES'):
            self.assertNotIn(key, env)
        self.assertEqual(env['CARGO_NET_OFFLINE'], 'true')
        self.assertEqual(env['npm_config_offline'], 'true')
        self.assertEqual(env['MACOSX_DEPLOYMENT_TARGET'], '14.4')

    def test_image_format_or_link_mismatch_removes_owned_output(self):
        for role in self.images:
            original = self.images[role]
            self.images[role] = b'PRIVATE invalid image'
            with self.subTest(role=role), self.assertRaises(tool.layout.LayoutError):
                tool.build(self.output)
            self.assertFalse(self.output.exists())
            self.images[role] = original

    def test_changed_source_and_changed_head_are_refused(self):
        self.mutate = lambda stage: (stage / self.input).write_bytes(b'changed')
        with self.assertRaisesRegex(tool.source.BuildError, 'BUILD_INPUT_CHANGED'):
            tool.build(self.output)
        self.assertFalse(self.output.exists())
        self.mutate = lambda stage: None
        with patch.object(tool.source, 'head', side_effect=[(self.revision, self.tree), ('d' * 40, self.tree)]):
            with self.assertRaisesRegex(tool.source.BuildError, 'SOURCE_CHANGED'):
                tool.build(self.output)
        self.assertFalse(self.output.exists())

    def test_existing_output_and_checkout_output_are_untouched(self):
        self.output.mkdir()
        (self.output / 'original').write_bytes(b'keep')
        with self.assertRaises(FileExistsError):
            tool.build(self.output)
        self.assertEqual((self.output / 'original').read_bytes(), b'keep')
        with self.assertRaisesRegex(tool.source.BuildError, 'OUTPUT_INSIDE_SOURCE'):
            tool.build(self.repo / 'new')
        self.assertEqual(self.calls, [])

    @unittest.skipUnless(os.name == 'posix', 'symlink fixture requires POSIX')
    def test_symlink_parent_is_refused(self):
        alias = self.root / 'alias'
        alias.symlink_to(self.repo, target_is_directory=True)
        with self.assertRaisesRegex(tool.source.BuildError, 'OUTPUT_PARENT_INVALID'):
            tool.build(alias / 'new')
        self.assertEqual(self.calls, [])

    def test_build_requires_disposable_arm64_mac(self):
        for system, arch, runner in [('Linux', 'arm64', 'github-hosted'),
                                    ('Darwin', 'x86_64', 'github-hosted'),
                                    ('Darwin', 'arm64', 'self-hosted')]:
            with patch.object(tool.platform, 'system', return_value=system), \
                 patch.object(tool.platform, 'machine', return_value=arch), \
                 patch.dict(os.environ, {'RUNNER_ENVIRONMENT': runner}):
                with self.assertRaisesRegex(tool.source.BuildError, 'GUI_CI_HOST_REQUIRED'):
                    tool.build(self.output)
        self.assertEqual(self.calls, [])

    def test_cli_cannot_override_role_flags_signing_or_execution(self):
        for option in ('--source', '--flags', '--target', '--execute', '--sign', '--producer'):
            stream = io.StringIO()
            with contextlib.redirect_stdout(stream):
                self.assertEqual(tool.main([option, 'PRIVATE']), 1)
            self.assertEqual(json.loads(stream.getvalue())['code'], 'ARGUMENT_INVALID')
            self.assertNotIn('PRIVATE', stream.getvalue())

    def test_false_success_or_fixture_promotions_are_refused(self):
        record = tool.build(self.output)
        for field, value in [('activation', 'enabled'), ('distribution', 'ready'), ('imageExecuted', True),
                             ('imageLoaded', True), ('producerIncluded', True), ('signingPerformed', True),
                             ('enrollmentAccessed', True), ('productionGateClosed', False),
                             ('rustCfg', ['resident_gui_fixture']), ('swiftFixtureDefines', ['RESIDENT_LAUNCH_FIXTURE']),
                             ('cryptographicAttestation', True), ('blockers', [])]:
            bad = copy.deepcopy(record)
            bad[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                check.validate(bad, self.revision, self.tree)


class FixedCompilerRecipeTests(unittest.TestCase):
    def test_compiler_commands_are_offline_normal_and_fixed_relative_link(self):
        with tempfile.TemporaryDirectory() as temporary:
            stage = Path(temporary)
            calls = []
            def command(args, cwd, env):
                calls.append((list(args), dict(env)))
                return ('\n'.join(sorted(tool.support.EXPORTS)) + '\n').encode() if args[0] == '/usr/bin/nm' else b''
            with patch.object(tool.source, 'command', side_effect=command):
                env = tool.support.environment(stage)
                products = tool.support.compile_images(stage, env, '/swift', '/sdk')
            self.assertEqual(len(calls), 6)
            self.assertEqual(calls[0][0], ['npm', 'ci', '--offline', '--ignore-scripts', '--no-audit', '--no-fund'])
            self.assertEqual(calls[1][0], ['npm', 'run', 'build'])
            swift = calls[3][0]
            self.assertIn('-no-toolchain-stdlib-rpath', swift)
            self.assertIn('arm64-apple-macosx14.4', swift)
            self.assertIn(tool.layout.NATIVE, swift)
            self.assertNotIn('-D', swift)
            cargo, env = calls[-1]
            self.assertEqual(cargo[:1 + 1 + len(tool.support.CARGO_FLAGS)], ['cargo', 'build', *tool.support.CARGO_FLAGS])
            flags = env['CARGO_ENCODED_RUSTFLAGS'].split('\x1f')
            self.assertEqual(flags[:3], ['--cfg', 'resident_native_source', '-Dwarnings'])
            self.assertIn('link-arg=-Wl,-rpath,@executable_path/../Frameworks', flags)
            self.assertIn('link-arg=-Wl,-needed_library,' + str(stage / 'libResidentPreparation.dylib'), flags)
            self.assertEqual(set(products), {'gui', 'native'})
            self.assertFalse(any('codesign' in args[0] or args[0].endswith('rescue-host-shell') for args, _ in calls))


class GitSnapshotTests(unittest.TestCase):
    def test_exact_blobs_and_recipe_bytes_are_pinned(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            repo, stage = root / 'repo', root / 'snapshot'
            repo.mkdir()
            stage.mkdir()
            def git(*args):
                return subprocess.check_output(['git', '-c', 'user.name=Fixture',
                    '-c', 'user.email=fixture@example.invalid', *args], cwd=repo, stderr=subprocess.DEVNULL)
            git('init')
            paths = {'ui/tauri/package-lock.json', 'ui/tauri/src-tauri/Cargo.lock',
                     'ui/tauri/src-tauri/tauri.conf.json', 'ui/tauri/src-tauri/src/main.rs',
                     *(tool.support.NATIVE_ROOT + '/' + name for name in (*tool.support.SWIFT_FILES, 'ResidentPreparation.h')),
                     *tool.support.RECIPE}
            for name in paths:
                path = repo / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b'pinned\n')
            git('add', '.')
            git('commit', '-qm', 'Snapshot fixture')
            revision = git('rev-parse', 'HEAD').decode().strip()
            ignored = repo / 'ui/tauri/ignored.txt'
            ignored.write_bytes(b'PRIVATE')
            with patch.object(tool.source, 'ROOT', repo):
                files, recipe = tool.support.snapshot(revision, stage)
                self.assertEqual(set(files), {p for p in paths if p.startswith('ui/')})
                self.assertFalse((stage / 'ui/tauri/ignored.txt').exists())
                self.assertEqual(set(recipe), set(tool.support.RECIPE))
                for name, digest in files.items():
                    self.assertEqual(digest, tool.source.digest((stage / name).read_bytes()))
                (repo / tool.support.RECIPE[0]).write_bytes(b'changed')
                with self.assertRaisesRegex(tool.source.BuildError, 'RECIPE_CHANGED'):
                    tool.support.snapshot(revision, stage)
                (repo / tool.support.RECIPE[0]).write_bytes(b'pinned\n')
                # A symlink entry is rejected without requiring OS symlink privileges.
                blob = git('rev-parse', revision + ':ui/tauri/package-lock.json')
                git('update-index', '--add', '--cacheinfo', '120000,' + blob.decode().strip() + ',ui/tauri/alias')
                git('commit', '-qm', 'Rejected link fixture')
                with self.assertRaisesRegex(tool.source.BuildError, 'GUI_SOURCE_INVALID'):
                    tool.support.snapshot(git('rev-parse', 'HEAD').decode().strip(), stage)


if __name__ == '__main__':
    unittest.main()
