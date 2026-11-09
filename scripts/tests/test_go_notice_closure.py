"""Synthetic producer/notice inputs only; no Go, signing, network or execution."""
import copy
import hashlib
import json
import os
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import build_mac_sealed_producer as builder
import collect_go_licenses as licenses
import package_mac_agent as mac
from go_ci import product_files_for

ROOT = Path(__file__).resolve().parents[2]
TOOL = ROOT / 'scripts/verify_go_notice_closure.py'
SOURCE = 'a' * 40


def sha(data):
    return hashlib.sha256(data).hexdigest()


class GoNoticeClosureTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name).resolve()
        self.producer = self.root / 'producer'
        self.notices = self.root / 'notices'
        self.module = self.root / 'module'
        for path in (self.producer, self.notices, self.module):
            path.mkdir()
        self.locks = {name: (ROOT / 'backend' / name).read_bytes() for name in ('go.mod', 'go.sum')}
        for name, data in self.locks.items():
            (self.module / name).write_bytes(data)
        texts = json.loads((ROOT / 'scripts/tests/fixtures/go-licenses/texts.json').read_text())
        for relative in mac.TEXTS:
            path = self.notices / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(texts[path.name].encode())
        self.inventory = licenses.inventory_record(SOURCE, self.locks)
        self.image = (struct.pack('<8I', 0xfeedfacf, 0x100000c, 0, 2, 1, 24, 0, 0)
                      + struct.pack('<6I', 0x32, 24, 1, 27 << 16, 27 << 16, 0))
        (self.producer / 'resident-pipe').write_bytes(self.image)
        files = {'backend/' + package[len(builder.source.PROJECT):].lstrip('/') + '/' + name: 'c'*64
                 for package, names in product_files_for('darwin', True).items() for name in names}
        self.record = {
            'schema': 'tether-mac-sealed-producer-build/1', 'repository': 'O2-CONNECTIVE/Tether',
            'status': 'blocked', 'activation': 'blocked', 'distribution': 'blocked',
            'sourceCommit': SOURCE, 'sourceTree': 'b'*40, 'buildUTC': '2026-11-09T12:00:00Z',
            'target': 'darwin-arm64', 'role': builder.ROLE, 'flags': list(builder.FLAGS),
            'settings': {'GOVERSION': 'go1.27.3', 'GOOS': 'darwin', 'GOARCH': 'arm64', 'CGO_ENABLED': '1'},
            'producerExecutable': True, 'producerExecuted': False, 'signingPerformed': False,
            'enrollmentAccessed': False, 'cryptographicAttestation': False,
            'blockers': list(builder.BLOCKERS), 'snapshotGoFiles': files,
            'locks': {'backend/' + name: sha(data) for name, data in self.locks.items()},
            'recipe': {path: 'd'*64 for path in builder.RECIPE},
            'compiledRepositoryPackages': [builder.ROLE],
            'modules': [dict(zip(('Path', 'Version', 'Sum', 'GoModSum'), (name, *values)))
                        for name, values in licenses.MODULES.items()],
            'image': {'path': 'resident-pipe', 'kind': 'mach-o-executable', 'sha256': sha(self.image),
                      'bytes': len(self.image), 'header': mac.macho(self.image, 'darwin-arm64')},
        }
        self.save()

    def save(self):
        self.record_bytes = (json.dumps(self.record, sort_keys=True) + '\n').encode()
        (self.producer / 'BUILD_RECORD.json').write_bytes(self.record_bytes)
        (self.notices / 'DEPENDENCIES.json').write_text(json.dumps(self.inventory), encoding='utf-8')

    def run_cli(self, *extra):
        return subprocess.run([sys.executable, str(TOOL), '--producer-root', str(self.producer),
            '--notices-root', str(self.notices), '--module-root', str(self.module), '--source', SOURCE,
            '--build-record-sha256', sha(self.record_bytes), '--producer-sha256', sha(self.image), *extra],
            capture_output=True, text=True, timeout=15, env={**os.environ, 'PATH': '', 'PYTHONOPTIMIZE': '2'})

    def rejected(self, code=None, *extra):
        result = self.run_cli(*extra)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertEqual(result.stderr, '')
        value = json.loads(result.stdout)
        self.assertEqual(value['status'], 'refused')
        if code:
            self.assertEqual(value['code'], code)
        self.assertNotIn(str(self.root), result.stdout)
        self.assertNotIn('PRIVATE', result.stdout)

    def test_matching_record_and_seven_texts_remain_blocked(self):
        result = self.run_cli()
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertEqual(result.stderr, '')
        value = json.loads(result.stdout)
        self.assertIs(value['selectedModuleNoticesMatched'], True)
        self.assertEqual(value['selectedModuleCount'], 3)
        self.assertEqual(value['fixedModulesNotSelected'], [])
        self.assertEqual(value['distributionStatus'], 'blocked')
        self.assertIs(value['productionGateClosed'], True)
        self.assertIs(value['binaryDependenciesVerified'], False)
        self.assertIs(value['noticeCompletenessVerified'], False)
        self.assertIn('stdlib-vendored-notices', value['unverifiedScopes'])
        self.assertIn('source-and-recipe-correspondence', value['unverifiedScopes'])

    def test_selected_subset_is_not_confused_with_all_locked_modules(self):
        self.record['modules'] = self.record['modules'][:1]
        self.save()
        result = self.run_cli()
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        value = json.loads(result.stdout)
        self.assertEqual(value['selectedModuleCount'], 1)
        self.assertEqual(value['fixedModulesNotSelected'], ['golang.org/x/crypto', 'golang.org/x/sys'])

    def test_module_outside_fixed_notice_set_is_refused(self):
        self.record['modules'].append({'Path': 'PRIVATE/unknown', 'Version': 'v1.0.0',
                                       'Sum': 'h1:unknown', 'GoModSum': 'h1:unknown'})
        self.save()
        self.rejected('GO_NOTICE_MODULE_UNCOVERED')


    def test_all_declared_modules_can_be_unused_without_claiming_complete_closure(self):
        self.record['modules'] = []
        self.save()
        result = self.run_cli()
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        value = json.loads(result.stdout)
        self.assertEqual(value['selectedModuleCount'], 0)
        self.assertEqual(value['fixedModulesNotSelected'], sorted(licenses.MODULES))
        self.assertIs(value['binaryDependenciesVerified'], False)
        self.assertIn('recorded-module-closure-completeness', value['unverifiedScopes'])

    def test_changed_duplicate_replaced_or_local_modules_are_refused(self):
        original = copy.deepcopy(self.record['modules'])
        cases = [lambda m: m[0].update(Version='v9.0.0'),
                 lambda m: m[0].update(Sum='h1:PRIVATE'),
                 lambda m: m[0].update(GoModSum='h1:PRIVATE'),
                 lambda m: m[0].update(Replace=None),
                 lambda m: m[0].update(Main=True),
                 lambda m: m[0].update(Dir='/PRIVATE/cache'),
                 lambda m: m[0].update(Path=[]), lambda m: m[0].pop('Sum'),
                 lambda m: m.append(m[0].copy())]
        for change in cases:
            with self.subTest(change=change):
                self.record['modules'] = copy.deepcopy(original)
                change(self.record['modules'])
                self.save()
                self.rejected()

    def test_build_identity_flags_and_false_boundary_promotions_are_refused(self):
        original = copy.deepcopy(self.record)
        cases = [('schema', 'rescue-mac-resident-library-build/1'), ('sourceCommit', 'b'*40),
                 ('sourceTree', 'PRIVATE'), ('status', 'ready'), ('activation', 'enabled'),
                 ('distribution', 'allowed'), ('target', 'darwin-amd64'), ('role', 'PRIVATE'),
                 ('flags', []), ('settings', {}), ('producerExecutable', 1),
                 ('producerExecuted', True), ('signingPerformed', 0), ('enrollmentAccessed', True),
                 ('cryptographicAttestation', True), ('blockers', []), ('recipe', {}),
                 ('snapshotGoFiles', {}), ('locks', {}), ('compiledRepositoryPackages', []),
                 ('compiledRepositoryPackages', [builder.ROLE, builder.ROLE]),
                 ('compiledRepositoryPackages', [builder.ROLE, 'PRIVATE']),
                 ('buildUTC', '2026-02-30T12:00:00Z'), ('buildUTC', '2026-11-09'),
                 ('modules', {}), ('image', {})]
        for field, value in cases:
            with self.subTest(field=field, value=value):
                self.record = copy.deepcopy(original)
                self.record[field] = value
                self.save()
                self.rejected()
        self.record = copy.deepcopy(original)
        self.record['PRIVATE'] = 'not a supported field'
        self.save()
        self.rejected()

    def test_inventory_source_locks_and_false_claims_are_refused(self):
        original = copy.deepcopy(self.inventory)
        cases = [('sourceCommit', 'b'*40), ('lockFiles', {}), ('moduleGraphVerified', True),
                 ('binaryDependenciesVerified', True), ('sourceCommitVerified', 0),
                 ('productionGateClosed', 1), ('distributionStatus', 'ready'),
                 ('modules', []), ('scope', 'complete'), ('toolchain', {})]
        for field, value in cases:
            with self.subTest(field=field):
                self.inventory = copy.deepcopy(original)
                self.inventory[field] = value
                self.save()
                self.rejected('GO_NOTICE_INVENTORY_MISMATCH')

    def test_each_fixed_text_tamper_and_missing_file_are_refused(self):
        for relative in mac.TEXTS:
            path = self.notices / relative
            original = path.read_bytes()
            for data in (b'PRIVATE changed text', None):
                with self.subTest(path=relative, missing=data is None):
                    if data is None:
                        path.unlink()
                    else:
                        path.write_bytes(data)
                    self.rejected()
                    path.write_bytes(original)

    def test_notice_inventory_cannot_substitute_self_declared_text_hash(self):
        path = self.notices / 'websocket-v1.8.15/LICENSE.txt'
        path.write_bytes(b'PRIVATE changed text')
        self.inventory['modules'][0]['texts']['LICENSE.txt']['sha256'] = sha(path.read_bytes())
        self.save()
        self.rejected('GO_NOTICE_INVENTORY_MISMATCH')

    def test_record_and_image_external_hashes_are_checked(self):
        self.rejected('GO_NOTICE_BUILD_HASH_MISMATCH', '--build-record-sha256', '0'*64)
        self.rejected('GO_NOTICE_IMAGE_HASH_MISMATCH', '--producer-sha256', '0'*64)
        (self.producer / 'resident-pipe').write_bytes(self.image + b'PRIVATE')
        self.rejected('GO_NOTICE_IMAGE_HASH_MISMATCH')

    def test_image_record_hash_size_header_and_kind_are_checked(self):
        original = copy.deepcopy(self.record['image'])
        for field, value in [('path', '../PRIVATE'), ('sha256', '0'*64), ('bytes', 0),
                             ('header', {}), ('kind', 'go-library-archive')]:
            with self.subTest(field=field):
                self.record['image'] = copy.deepcopy(original)
                self.record['image'][field] = value
                self.save()
                self.rejected('GO_NOTICE_IMAGE_RECORD_MISMATCH')

    def test_archive_wrong_cpu_and_non_executable_images_are_refused(self):
        for data in (b'!<arch>\n', self.image[:4] + struct.pack('<I', 0x1000007) + self.image[8:],
                     self.image[:12] + struct.pack('<I', 6) + self.image[16:]):
            self.image = data
            (self.producer / 'resident-pipe').write_bytes(data)
            self.record['image']['sha256'] = sha(data)
            self.record['image']['bytes'] = len(data)
            self.save()
            self.rejected()

    def test_lock_content_and_record_lock_hash_must_both_match(self):
        (self.module / 'go.mod').write_bytes(self.locks['go.mod'] + b'\nreplace PRIVATE => ./PRIVATE\n')
        self.rejected('GO_NOTICE_MATERIAL_INVALID')
        (self.module / 'go.mod').write_bytes(self.locks['go.mod'])
        self.record['locks']['backend/go.sum'] = '0'*64
        self.save()
        self.rejected('GO_NOTICE_LOCK_MISMATCH')
        self.record['locks']['backend/go.sum'] = sha(self.locks['go.sum'])
        self.save()
        # Same semantics, different bytes must not silently match a stale record.
        (self.module / 'go.mod').write_bytes(self.locks['go.mod'] + b'\n')
        self.rejected('GO_NOTICE_LOCK_MISMATCH')

    def test_extra_files_empty_directories_and_missing_records_are_refused(self):
        for root in (self.producer, self.notices):
            for directory in (False, True):
                path = root / 'PRIVATE-extra'
                path.mkdir() if directory else path.write_bytes(b'PRIVATE')
                self.rejected('GO_NOTICE_MATERIAL_INVALID')
                path.rmdir() if directory else path.unlink()
        for path in (self.producer / 'BUILD_RECORD.json', self.notices / 'DEPENDENCIES.json'):
            original = path.read_bytes()
            path.unlink()
            self.rejected()
            path.write_bytes(original)

    @unittest.skipUnless(os.name == 'posix', 'POSIX symlink fixture; Windows reparse needs native testing')
    def test_symlink_files_directories_and_ancestor_aliases_are_refused(self):
        path = self.notices / 'websocket-v1.8.15/LICENSE.txt'
        original = path.read_bytes()
        path.unlink()
        target = self.root / 'PRIVATE-text'
        target.write_bytes(original)
        path.symlink_to(target)
        self.rejected('GO_NOTICE_MATERIAL_INVALID')
        path.unlink()
        path.write_bytes(original)
        directory = self.notices / 'crypto-v0.58.0'
        moved = self.root / 'PRIVATE-directory'
        directory.rename(moved)
        directory.symlink_to(moved, target_is_directory=True)
        self.rejected('GO_NOTICE_MATERIAL_INVALID')
        directory.unlink()
        moved.rename(directory)
        alias = self.root / 'PRIVATE-alias'
        alias.symlink_to(self.root, target_is_directory=True)
        self.rejected('GO_NOTICE_MATERIAL_INVALID', '--producer-root', str(alias / 'producer'))

    @unittest.skipUnless(os.name == 'posix', 'POSIX FIFO fixture; no FIFO creation on Windows')
    def test_fifo_is_refused_without_opening_or_hanging(self):
        path = self.producer / 'resident-pipe'
        path.unlink()
        os.mkfifo(path)
        self.rejected('GO_NOTICE_MATERIAL_INVALID')

    def test_duplicate_nested_keys_malformed_json_and_size_bounds_do_not_leak(self):
        for target, original in [(self.producer / 'BUILD_RECORD.json', self.record_bytes),
                                 (self.notices / 'DEPENDENCIES.json', json.dumps(self.inventory).encode())]:
            for data in (b'{"PRIVATE":1,"PRIVATE":2}', b'{"nested":{"PRIVATE":1,"PRIVATE":2}}',
                         b'{"PRIVATE":NaN}', b'[]', b'{"PRIVATE":', b'\xff',
                         b' '*(256*1024 + 1), b'['*2000 + b']'*2000):
                with self.subTest(target=target.name, kind=data[:20]):
                    target.write_bytes(data)
                    if target.name == 'BUILD_RECORD.json':
                        self.record_bytes = data
                    self.rejected()
            target.write_bytes(original)
            if target.name == 'BUILD_RECORD.json':
                self.record_bytes = original

    def test_invalid_expectations_arguments_and_oversize_notice_are_refused(self):
        for flag in ('--source', '--build-record-sha256', '--producer-sha256'):
            self.rejected('GO_NOTICE_EXPECTATION_INVALID', flag, 'PRIVATE')
        self.rejected('GO_NOTICE_ARGUMENT_INVALID', '--config', '/PRIVATE')
        (self.notices / 'websocket-v1.8.15/LICENSE.txt').write_bytes(b'x'*(16*1024 + 1))
        self.rejected('GO_NOTICE_MATERIAL_INVALID')

    def test_checker_does_not_modify_or_create_files(self):
        before = {p.relative_to(self.root): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        self.assertEqual(self.run_cli().returncode, 2)
        after = {p.relative_to(self.root): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        self.assertEqual(before, after)


if __name__ == '__main__':
    unittest.main()
