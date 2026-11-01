"""固定本文収集CLIのfile境界。実binaryや配布可能性を試験しない。"""

import hashlib
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
CLI = ROOT / 'scripts/collect_go_licenses.py'
FIXTURE = Path(__file__).parent / 'fixtures/go-licenses/texts.json'
SOURCE = 'a' * 40
LICENSE_SHA = '911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad'
WEBSOCKET_SHA = 'cc0975a5f6305145bdd7b41ce9479632fdac3870e6ac4281f28017f18c767c4e'
PATENTS_SHA = '96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc'
MODULES = [
    {'Path': 'github.com/coder/websocket', 'Version': 'v1.8.15',
     'Sum': 'h1:6B2JPeOGlpff2Uz6vOEH1Vzpi0iUz20A+lPVhPHtNUA=',
     'GoModSum': 'h1:NX3SzP+inril6yawo5CQXx8+fk145lPDC6pumgx0mVg='},
    {'Path': 'golang.org/x/crypto', 'Version': 'v0.58.0',
     'Sum': 'h1:COkYLr4k7nDI4r6QtMML2AfzeIswWHAJthLS6K/M/54=',
     'GoModSum': 'h1:Xh+kl5A+M0gMiWDytH2Hhr5Z7WbNCpv/eB/EFgfSk+w='},
    {'Path': 'golang.org/x/sys', 'Version': 'v0.49.0',
     'Sum': 'h1:XbzkgYJHdqh/8m2Uu0W/dQv8nktxx4BFHp1M0gROTXA=',
     'GoModSum': 'h1:hNLxWAXmnKAxqDtdwIYC4bM9oQPEecfsnNMuSxOs3og='},
]


class GoLicenseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name)
        self.module = self.base / 'module'
        self.cache = self.base / 'cache'
        self.toolchain = self.base / 'go'
        self.output = self.base / 'collected'
        self.metadata = self.base / 'modules.json'
        self.module.mkdir()
        self.toolchain.mkdir()
        for name in ('go.mod', 'go.sum'):
            (self.module / name).write_bytes((ROOT / 'backend' / name).read_bytes())
        (self.toolchain / 'VERSION').write_bytes(b'go1.27.3\ntime 2026-10-20T00:00:00Z\n')
        self.texts = json.loads(FIXTURE.read_text(encoding='utf-8'))
        for folder in [self.toolchain] + [self.cache / (m['Path'] + '@' + m['Version'])
                                          for m in MODULES]:
            folder.mkdir(parents=True, exist_ok=True)
            for name, value in self.component_texts(folder).items():
                (folder / name).write_bytes(value.encode('utf-8'))
        self.write_metadata(MODULES)

    def component_texts(self, folder):
        names = ('LICENSE.txt',) if folder.name == 'websocket@v1.8.15' else ('LICENSE', 'PATENTS')
        return {name: self.texts[name] for name in names}

    def write_metadata(self, modules):
        self.metadata.write_text('\n'.join(json.dumps(item) for item in modules), encoding='utf-8')

    def run_cli(self, extra=()):
        return subprocess.run([sys.executable, '-B', str(CLI), '--module-root', str(self.module),
                               '--module-cache', str(self.cache), '--go-root', str(self.toolchain),
                               '--metadata', str(self.metadata), '--source', SOURCE,
                               '--output', str(self.output), *extra],
                              capture_output=True, text=True, timeout=10,
                              env={**os.environ, 'PATH': ''})

    def assert_rejected(self, code=None, extra=()):
        result = self.run_cli(extra)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertEqual(result.stderr, '')
        record = json.loads(result.stdout)
        self.assertEqual(record['status'], 'fail')
        if code is not None:
            self.assertEqual(record['code'], code)
        self.assertNotIn(str(self.base), result.stdout)
        self.assertNotIn('PRIVATE', result.stdout)
        self.assertFalse(self.output.exists())

    def test_unknown_module_is_rejected_before_creating_output(self):
        self.write_metadata([*MODULES, {'Path': 'PRIVATE/unknown', 'Version': 'v1.0.0'}])
        self.assert_rejected('GO_LICENSE_MODULE_SET_INVALID')

    def test_changed_license_is_rejected_even_with_unchanged_module_checksum(self):
        (self.cache / 'golang.org/x/crypto@v0.58.0/LICENSE').write_bytes(b'PRIVATE changed text')
        self.assert_rejected('GO_LICENSE_TEXT_HASH_MISMATCH')

    def test_symlink_license_is_rejected_even_when_target_has_expected_bytes(self):
        license_file = self.toolchain / 'LICENSE'
        license_file.unlink()
        target = self.base / 'PRIVATE-outside-license'
        target.write_bytes(self.texts['LICENSE'].encode())
        license_file.symlink_to(target)
        self.assert_rejected('GO_LICENSE_FILE_INVALID')

    # 以下は各RED→GREEN後に追加した入力境界の回帰。
    def test_rejects_replaced_local_duplicate_missing_and_changed_module_metadata(self):
        cases = [
            ('replace', lambda m: m[0].update(Replace={'Dir': 'PRIVATE/outside'})),
            ('null-replace', lambda m: m[0].update(Replace=None)),
            ('local', lambda m: m[0].update(Main=True)),
            ('local-path', lambda m: m[0].update(Path='../PRIVATE')),
            ('duplicate', lambda m: m.append(m[0].copy())),
            ('missing', lambda m: m.pop()),
            ('version', lambda m: m[0].update(Version='v0.57.0')),
            ('sum', lambda m: m[0].update(Sum=MODULES[1]['Sum'])),
            ('mod-sum', lambda m: m[0].update(GoModSum=MODULES[1]['GoModSum'])),
            ('unknown-field', lambda m: m[0].update(Private='PRIVATE')),
            ('missing-field', lambda m: m[0].pop('Sum')),
            ('wrong-type', lambda m: m[0].update(Indirect='true')),
            ('empty', lambda m: m.clear()),
        ]
        for name, change in cases:
            with self.subTest(case=name):
                modules = copy.deepcopy(MODULES)
                change(modules)
                self.write_metadata(modules)
                self.assert_rejected()

    def test_metadata_directory_fields_never_select_input_or_leak_into_inventory(self):
        modules = copy.deepcopy(MODULES)
        for module in modules:
            module.update(Dir=str(self.base / 'PRIVATE-never-read'), GoMod='/PRIVATE/go.mod',
                          Time='2026-10-01T00:00:00Z', GoVersion='1.26.0', Indirect=False)
        self.write_metadata(modules)
        result = self.run_cli()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn('PRIVATE', (self.output / 'DEPENDENCIES.json').read_text())

    def test_metadata_is_bounded_strict_json_without_raw_error_output(self):
        normal = self.metadata.read_bytes()
        for data in [normal.replace(b'{', b'{"Path":"PRIVATE",', 1),
                     b'{"Path":"PRIVATE","\\u0050ath":"golang.org/x/crypto"}',
                     b'PRIVATE not json', b'\xff', b' ' * (64 * 1024 + 1),
                     b'[' + b'[' * 10000, json.dumps(MODULES).encode()]:
            with self.subTest(size=len(data)):
                self.metadata.write_bytes(data)
                self.assert_rejected()

    def test_locks_reject_added_replaced_or_changed_dependencies(self):
        paths = [self.module / 'go.mod', self.module / 'go.sum']
        original = {path: path.read_bytes() for path in paths}
        cases = [
            (paths[0], original[paths[0]] + b'\nreplace golang.org/x/crypto => ../PRIVATE\n'),
            (paths[0], original[paths[0]] + b'\nrequire PRIVATE/module v1.0.0\n'),
            (paths[0], original[paths[0]].replace(b'1.27.3', b'1.27.2')),
            (paths[0], original[paths[0]].replace(b'v0.58.0', b'v0.57.0')),
            (paths[1], original[paths[1]] + original[paths[1]].splitlines()[0] + b'\n'),
            (paths[1], b'\n'.join(original[paths[1]].splitlines()[1:])),
            (paths[1], original[paths[1]].replace(b'h1:COk', b'h1:XXX')),
        ]
        for path, data in cases:
            with self.subTest(file=path.name, size=len(data)):
                for source, value in original.items():
                    source.write_bytes(value)
                path.write_bytes(data)
                self.assert_rejected('GO_LICENSE_LOCK_MISMATCH')

    def test_missing_empty_changed_and_nonregular_texts_do_not_create_output(self):
        locations = [self.toolchain, self.cache / 'golang.org/x/crypto@v0.58.0',
                     self.cache / 'golang.org/x/sys@v0.49.0',
                     self.cache / 'github.com/coder/websocket@v1.8.15']
        for folder in locations:
            for name in self.component_texts(folder):
                path = folder / name
                for fault in ['missing', 'empty', 'changed', 'directory', 'oversize']:
                    with self.subTest(component=folder.name, text=name, fault=fault):
                        path.unlink()
                        if fault == 'directory':
                            path.mkdir()
                        elif fault != 'missing':
                            path.write_bytes({'empty': b'', 'changed': b'PRIVATE',
                                              'oversize': b'x' * (16 * 1024 + 1)}[fault])
                        self.assert_rejected()
                        if path.is_dir():
                            path.rmdir()
                        path.write_bytes(self.texts[name].encode())

    def test_symlink_roots_nested_directories_and_metadata_are_rejected(self):
        for path in [self.toolchain, self.module, self.cache, self.cache / 'golang.org',
                     self.cache / 'golang.org/x/crypto@v0.58.0',
                     self.cache / 'github.com/coder/websocket@v1.8.15', self.metadata]:
            with self.subTest(component=path.name):
                target = path.with_name(path.name + '-PRIVATE-moved')
                path.rename(target)
                path.symlink_to(target, target_is_directory=target.is_dir())
                self.assert_rejected('GO_LICENSE_FILE_INVALID')
                path.unlink()
                target.rename(path)

    def test_version_lock_and_metadata_size_limits_are_finite(self):
        for path, limit in [(self.toolchain / 'VERSION', 512),
                            (self.module / 'go.mod', 16 * 1024),
                            (self.module / 'go.sum', 16 * 1024),
                            (self.metadata, 64 * 1024)]:
            with self.subTest(file=path.name):
                original = path.read_bytes()
                path.write_bytes(b'x' * (limit + 1))
                self.assert_rejected('GO_LICENSE_SIZE_LIMIT')
                path.write_bytes(original)

    def test_other_toolchain_version_and_invalid_source_are_rejected(self):
        version = self.toolchain / 'VERSION'
        original = version.read_bytes()
        version.write_bytes(b'go1.27.2\n')
        self.assert_rejected('GO_LICENSE_TOOLCHAIN_VERSION_MISMATCH')
        version.write_bytes(original)
        for source in ['PRIVATE', 'a' * 39, 'A' * 40]:
            self.assert_rejected('GO_LICENSE_SOURCE_INVALID', ['--source', source])

    def test_exact_module_set_supports_individual_and_block_requires(self):
        path = self.module / 'go.mod'
        prefix = 'module github.com/O2-CONNECTIVE/rescue-host/backend\ngo 1.27.3\n'
        entries = [m['Path'] + ' ' + m['Version'] for m in MODULES]
        for body in [''.join('require ' + entry + '\n' for entry in entries),
                     'require (\n' + '\n'.join(reversed(entries)) + '\n)\n']:
            with self.subTest(body=body):
                path.write_text(prefix + body)
                result = self.run_cli()
                self.assertEqual(result.returncode, 0, result.stdout)
                # Successful destinations are intentionally never reused.
                self.output = self.base / (self.output.name + '-next')
        for body in ['require ()', 'require (\n' + '\n'.join(entries) + '\n',
                     'require (\n' + '\n'.join(entries + [entries[0]]) + '\n)',
                     'require ' + entries[0], 'require (\n' + '\n'.join(entries) + '\n)\nexclude x v1.0.0',
                     'require (\n' + '\n'.join(entries) + '\n)\nrequire PRIVATE/module v1.0.0']:
            with self.subTest(body=body):
                path.write_text(prefix + body)
                self.assert_rejected('GO_LICENSE_LOCK_MISMATCH')

    def test_existing_output_file_directory_and_symlink_are_not_overwritten(self):
        for kind in ['file', 'directory', 'symlink']:
            with self.subTest(kind=kind):
                if kind == 'file':
                    self.output.write_bytes(b'PRIVATE-existing')
                elif kind == 'directory':
                    self.output.mkdir()
                    (self.output / 'keep').write_bytes(b'PRIVATE-existing')
                else:
                    self.output.symlink_to(self.base / 'PRIVATE-missing')
                result = self.run_cli()
                self.assertEqual(result.returncode, 1, result.stdout)
                self.assertEqual(json.loads(result.stdout)['code'], 'GO_LICENSE_OUTPUT_EXISTS')
                self.assertEqual(result.stderr, '')
                if kind == 'directory':
                    self.assertEqual((self.output / 'keep').read_bytes(), b'PRIVATE-existing')
                    (self.output / 'keep').unlink()
                    self.output.rmdir()
                else:
                    if kind == 'file':
                        self.assertEqual(self.output.read_bytes(), b'PRIVATE-existing')
                    else:
                        self.assertTrue(self.output.is_symlink())
                    self.output.unlink()

    def test_argument_error_is_fixed_code_and_help_does_not_collect(self):
        self.assert_rejected('GO_LICENSE_ARGUMENT_INVALID', ['--PRIVATE-secret', '/PRIVATE/path'])
        result = subprocess.run([sys.executable, '-B', str(CLI), '--help'],
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0)
        self.assertIn('--metadata', result.stdout)
        self.assertFalse(self.output.exists())

    def test_collects_seven_pinned_texts_and_records_checks_without_distribution_claim(self):
        result = self.run_cli()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stderr, '')
        self.assertEqual(json.loads(result.stdout), {
            'check': 'go-license-inventory', 'status': 'collected', 'textCount': 7,
            'distributionStatus': 'blocked', 'productionGateClosed': True})
        inventory = json.loads((self.output / 'DEPENDENCIES.json').read_text(encoding='utf-8'))
        self.assertEqual(inventory['sourceCommit'], SOURCE)
        self.assertFalse(inventory['sourceCommitVerified'])
        self.assertEqual(inventory['toolchain']['version'], 'go1.27.3')
        self.assertEqual(inventory['toolchain']['archiveVerified'], False)
        self.assertEqual(inventory['scope'], 'fixed-direct-modules-and-toolchain-texts-only')
        self.assertFalse(inventory['moduleGraphVerified'])
        self.assertFalse(inventory['binaryDependenciesVerified'])
        self.assertEqual([p['module'] for p in inventory['modules']],
                         ['github.com/coder/websocket', 'golang.org/x/crypto', 'golang.org/x/sys'])
        expected_paths = {'DEPENDENCIES.json'}
        for name in ('go.mod', 'go.sum'):
            self.assertEqual(inventory['lockFiles'][name],
                             hashlib.sha256((self.module / name).read_bytes()).hexdigest())
        for component in [inventory['toolchain'], *inventory['modules']]:
            is_websocket = component.get('module') == 'github.com/coder/websocket'
            if is_websocket:
                self.assertEqual(component['source'], 'https://github.com/coder/websocket/tree/v1.8.15')
                expected_texts = [('LICENSE.txt', WEBSOCKET_SHA)]
            else:
                self.assertTrue(component['source'].startswith('https://github.com/golang/'))
                expected_texts = [('LICENSE', LICENSE_SHA), ('PATENTS', PATENTS_SHA)]
            self.assertEqual(set(component['texts']), {name for name, _ in expected_texts})
            for name, expected in expected_texts:
                record = component['texts'][name]
                expected_paths.add(record['path'])
                self.assertEqual(record['sha256'], expected)
                self.assertEqual((self.output / record['path']).read_bytes(), self.texts[name].encode())
        self.assertEqual({p.relative_to(self.output).as_posix() for p in self.output.rglob('*')
                          if p.is_file()}, expected_paths)
        for module, expected in zip(inventory['modules'], MODULES):
            self.assertEqual(module['moduleSum'], expected['Sum'])
            self.assertEqual(module['goModSum'], expected['GoModSum'])
            self.assertFalse(module['directoryVerified'])
        self.assertNotIn(str(self.base), json.dumps(inventory))


if __name__ == '__main__':
    unittest.main()
