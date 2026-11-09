"""Finite material fixtures only: no real app, native execution, cache or upload."""
import copy
import json
import os
from pathlib import Path
import plistlib
import stat
import struct
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

from scripts import verify_mac_evaluation_candidate as candidate

ROOT = Path(__file__).resolve().parents[2]


def image(cpu=0x0100000c):
    commands = struct.pack('<6I', 0x32, 24, 1, 27 << 16, 27 << 16, 0)
    return struct.pack('<8I', 0xfeedfacf, cpu, 0, 2, 1, len(commands), 0, 0) + commands


class CandidateFixture:
    def __init__(self, root):
        self.root = root
        app = 'TetherEvaluationCandidate.app'
        self.images = {role: app + '/' + path for role, path in candidate.IMAGES.items()}
        self.hashes = {role: candidate.digest(image()) for role in self.images}
        texts = json.loads((ROOT / 'scripts/tests/fixtures/go-licenses/texts.json').read_text())
        files = {name: b'finite fixture only\n' for name in candidate.FIXED | candidate.GUI_NOTICES}
        files.update({name: texts[Path(name).name].encode() for name in candidate.GO_NOTICES})
        files['notices/gui/rust/example-1.0.0/LICENSE.txt'] = b'Synthetic license fixture\n'
        files.update({path: image() for path in self.images.values()})
        files[app + '/Contents/Info.plist'] = plistlib.dumps({
            'CFBundleExecutable': 'rescue-host-shell', 'CFBundleDisplayName': candidate.LABEL,
            'CFBundlePackageType': 'APPL'})
        self.record = {'schema': candidate.SCHEMA, 'repository': 'O2-CONNECTIVE/Tether',
                       'sourceCommit': 'a' * 40, 'buildUTC': '2026-11-09T00:00:00Z',
                       'target': 'darwin-arm64', 'label': candidate.LABEL, 'appDirectory': app,
                       'distributionStatus': 'blocked', 'signingStatus': 'unverified',
                       'productionGateClosed': True, 'unverifiedScopes': candidate.GAPS.copy(),
                       'images': {role: {'path': path, 'sha256': self.hashes[role]}
                                  for role, path in self.images.items()},
                       'files': {path: candidate.digest(value) for path, value in files.items()},
                       'notices': sorted(path for path in files if path.startswith('notices/')),
                       **{field: False for field in candidate.FLAGS}}
        for path, value in files.items():
            target = root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(value)
        self.save()

    def save(self):
        self.manifest = (json.dumps(self.record, sort_keys=True) + '\n').encode()
        (self.root / 'MANIFEST.json').write_bytes(self.manifest)
        self.manifest_hash = candidate.digest(self.manifest)

    def verify(self):
        return candidate.verify(self.root, 'a' * 40, self.manifest_hash, self.hashes)

    def cli(self, extra=()):
        return subprocess.run([sys.executable, str(ROOT / 'scripts/verify_mac_evaluation_candidate.py'),
            '--root', str(self.root), '--source', 'a' * 40,
            '--manifest-sha256', self.manifest_hash, '--gui-sha256', self.hashes['gui'],
            '--producer-sha256', self.hashes['producer'], *extra],
            capture_output=True, text=True, timeout=15)


class CandidateTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name).resolve()
        self.fixture = CandidateFixture(self.root)

    def refuse(self, code=None):
        if code:
            with self.assertRaisesRegex(candidate.CandidateError, code):
                self.fixture.verify()
        else:
            with self.assertRaises((candidate.CandidateError, candidate.mac.PackageError,
                                    OSError, ValueError, TypeError)):
                self.fixture.verify()

    def test_materials_match_but_cli_stays_blocked_and_read_only(self):
        before = {p.relative_to(self.root): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        result = self.fixture.verify()
        self.assertEqual(result['status'], 'blocked')
        self.assertIs(result['materialsMatched'], True)
        self.assertEqual(result['unverifiedScopes'], candidate.GAPS)
        self.assertEqual(result['platformDeclarations']['producer']['minimumMacOSDeclared'], '27.0.0')
        run = self.fixture.cli()
        self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
        self.assertEqual(run.stderr, '')
        self.assertEqual(json.loads(run.stdout), result)
        after = {p.relative_to(self.root): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        self.assertEqual(before, after)

    def test_independent_expected_hashes_and_source(self):
        for field, value in [('sourceCommit', 'b' * 40)]:
            self.fixture.record[field] = value
            self.fixture.save()
            self.refuse('CANDIDATE_SOURCE_MISMATCH')
        self.fixture.record['sourceCommit'] = 'a' * 40
        self.fixture.save()
        self.fixture.manifest_hash = '0' * 64
        self.refuse('CANDIDATE_MANIFEST_HASH_MISMATCH')
        self.fixture.save()
        self.fixture.hashes['producer'] = '0' * 64
        self.refuse('CANDIDATE_IMAGE_MISMATCH')

    def test_no_hash_only_source_or_release_claim_is_accepted(self):
        original = copy.deepcopy(self.fixture.record)
        changes = [('distributionStatus', 'ready'), ('signingStatus', 'signed'),
                   ('productionGateClosed', False), ('productionGateClosed', 1),
                   ('unverifiedScopes', []), ('repository', 'O2-CONNECTIVE/rescue-host'),
                   ('label', '1.0'), ('schema', 'future'), ('unexpected', 'PRIVATE')]
        changes += [(field, value) for field in candidate.FLAGS for value in (True, 0)]
        for field, value in changes:
            with self.subTest(field=field, value=value):
                self.fixture.record = copy.deepcopy(original)
                self.fixture.record[field] = value
                self.fixture.save()
                self.refuse()

    def test_invalid_real_dates_and_nonutc_or_freeform_metadata(self):
        for value in ('2026-02-29T00:00:00Z', '2026-11-09T25:00:00Z',
                      '2026-11-09T00:00:00+09:00', 'PRIVATE', 1, None):
            with self.subTest(value=value):
                self.fixture.record['buildUTC'] = value
                self.fixture.save()
                self.refuse('CANDIDATE_TIME_INVALID')

    def test_known_ci_wrapper_and_unsafe_app_paths_are_refused(self):
        for value in ('TetherCurrentGUIFixture.app', '../bad.app', '/bad.app',
                      'nested/Bad.app', 'Bad', 'CON.app', 'bad\\name.app', ''):
            with self.subTest(value=value):
                self.fixture.record['appDirectory'] = value
                self.fixture.save()
                self.refuse('CANDIDATE_APP_INVALID')

    def test_missing_extra_or_redirected_image_roles(self):
        original = copy.deepcopy(self.fixture.record)
        changes = [lambda r: r['images'].pop('producer'),
                   lambda r: r['images'].update(extra={}),
                   lambda r: r['images']['producer'].update(path='PRIVATE'),
                   lambda r: r['images']['producer'].update(source='PRIVATE'),
                   lambda r: r['files'].pop(self.fixture.images['gui']),
                   lambda r: r['files'].update({'PRIVATE': 'a' * 64})]
        for change in changes:
            self.fixture.record = copy.deepcopy(original)
            change(self.fixture.record)
            self.fixture.save()
            self.refuse()

    def test_notice_closure_is_exact_declared_set_not_real_dependency_proof(self):
        original = copy.deepcopy(self.fixture.record)
        changes = [lambda r: r['notices'].pop(),
                   lambda r: r['notices'].append(r['notices'][0]),
                   lambda r: r['notices'].append('../PRIVATE'),
                   lambda r: r['notices'].append('notices/gui/rust/x/main.py'),
                   lambda r: r['notices'].append('notices/unknown/LICENSE'),
                   lambda r: r['files'].update({next(iter(candidate.GO_NOTICES)): '0' * 64})]
        for change in changes:
            self.fixture.record = copy.deepcopy(original)
            change(self.fixture.record)
            self.fixture.save()
            self.refuse()
        self.fixture.record = copy.deepcopy(original)
        name = 'notices/go/stdlib-vendor/example/LICENSE'
        p = self.root / name; p.parent.mkdir(parents=True); p.write_bytes(b'finite fixture\n')
        self.fixture.record['notices'].append(name)
        self.fixture.record['files'][name] = candidate.digest(p.read_bytes())
        self.fixture.save()
        self.assertIn('stdlib-vendored-notice-completeness', self.fixture.verify()['unverifiedScopes'])

    def test_every_fixed_file_missing_or_tampered_is_refused(self):
        for name in list(self.fixture.record['files']):
            with self.subTest(name=name):
                path = self.root / name; original = path.read_bytes()
                path.write_bytes(b'PRIVATE replaced')
                self.refuse('CANDIDATE_FILE_HASH_MISMATCH')
                path.unlink()
                self.refuse('CANDIDATE_MEMBER_SET_MISMATCH')
                path.write_bytes(original)

    def test_extra_file_or_empty_directory_is_refused(self):
        extra = self.root / 'PRIVATE'; extra.write_text('secret')
        self.refuse('CANDIDATE_MEMBER_SET_MISMATCH')
        extra.unlink(); extra.mkdir()
        self.refuse('CANDIDATE_MEMBER_SET_MISMATCH')

    def test_macho_target_not_script_library_or_wrong_arch(self):
        name = self.fixture.images['producer']
        for value in (b'#!/bin/sh\n', image(0x01000007), b'!<arch>\n', b'PRIVATE'):
            with self.subTest(value=value):
                (self.root / name).write_bytes(value)
                wanted = candidate.digest(value)
                self.fixture.hashes['producer'] = wanted
                self.fixture.record['images']['producer']['sha256'] = wanted
                self.fixture.record['files'][name] = wanted
                self.fixture.save()
                self.refuse()

    def test_plist_gui_path_and_display_label_are_bound(self):
        name = self.fixture.record['appDirectory'] + '/Contents/Info.plist'
        for changes in ({'CFBundleExecutable': 'resident_gui_ci'},
                        {'CFBundleDisplayName': 'CI fixture'}, {'CFBundlePackageType': 'BNDL'}):
            record = plistlib.loads((self.root / name).read_bytes()); record.update(changes)
            value = plistlib.dumps(record); (self.root / name).write_bytes(value)
            self.fixture.record['files'][name] = candidate.digest(value)
            self.fixture.save()
            self.refuse('CANDIDATE_PLIST_MISMATCH')

    def test_text_is_nonempty_utf8_even_when_declared_hash_matches(self):
        for value in (b' ', b'\xff\xfe', b''):
            (self.root / 'LICENSE').write_bytes(value)
            self.fixture.record['files']['LICENSE'] = candidate.digest(value)
            self.fixture.save()
            self.refuse()

    def test_duplicate_json_and_private_error_are_fixed_codes(self):
        for value in (b'{"schema":1,"schema":2}', b'PRIVATE', b'[' * 1500):
            (self.root / 'MANIFEST.json').write_bytes(value)
            self.fixture.manifest_hash = candidate.digest(value)
            run = self.fixture.cli()
            self.assertEqual(run.returncode, 1)
            self.assertEqual(run.stderr, '')
            self.assertNotIn('PRIVATE', run.stdout)
            self.assertNotIn(str(self.root), run.stdout)
        run = self.fixture.cli(['--unexpected', 'PRIVATE'])
        self.assertEqual(run.returncode, 1)
        self.assertEqual(json.loads(run.stdout)['code'], 'CANDIDATE_ARGUMENT_INVALID')
        self.assertEqual(run.stderr, '')

    def test_symlink_member_ancestor_and_root_are_refused(self):
        target = self.root / 'LICENSE'; value = target.read_bytes()
        saved = self.root / 'saved'; target.rename(saved)
        try:
            target.symlink_to(saved)
        except OSError:
            self.skipTest('OS does not permit symlink creation; metadata rejection is tested separately')
        self.refuse()
        target.unlink(); saved.rename(target)
        directory = self.root / 'locks'; moved = self.root / 'original-locks'; directory.rename(moved)
        directory.symlink_to(moved, target_is_directory=True)
        self.refuse()
        directory.unlink(); moved.rename(directory)
        with tempfile.TemporaryDirectory() as other:
            link = Path(other).resolve() / 'root'; link.symlink_to(self.root, target_is_directory=True)
            with self.assertRaises(candidate.mac.PackageError):
                candidate.verify(link, 'a' * 40, self.fixture.manifest_hash, self.fixture.hashes)
        self.assertEqual(target.read_bytes(), value)

    def test_special_member_metadata_refused_before_read(self):
        fifo = mock.Mock(st_mode=stat.S_IFIFO | 0o600, st_file_attributes=0)
        with mock.patch.object(candidate.mac, 'directory_chain'), mock.patch.object(Path, 'lstat', return_value=fifo), mock.patch.object(candidate.mac.os, 'open') as opened:
            with self.assertRaises(candidate.mac.PackageError):
                candidate.mac.read_regular(self.root / 'MANIFEST.json', candidate.MANIFEST_LIMIT)
            opened.assert_not_called()

    def test_size_bounds_and_manifest_change(self):
        with mock.patch.object(candidate, 'IMAGE_LIMIT', 8):
            self.refuse()
        with mock.patch.object(candidate, 'TOTAL_LIMIT', 1):
            self.refuse('CANDIDATE_SIZE_LIMIT')
        with mock.patch.object(candidate, 'FILE_LIMIT', 1):
            self.refuse('CANDIDATE_FILE_SET_INVALID')
        original = candidate.mac.read_regular
        reads = 0
        def read(path, limit):
            nonlocal reads
            data = original(path, limit)
            if path.name == 'MANIFEST.json':
                reads += 1
                if reads == 2:
                    return data + b' '
            return data
        with mock.patch.object(candidate.mac, 'read_regular', side_effect=read):
            self.refuse('CANDIDATE_MANIFEST_CHANGED')

    def test_case_alias_paths_are_refused(self):
        name = 'notices/gui/rust/example-1.0.0/license.txt'
        (self.root / name).write_bytes(b'fixture')
        self.fixture.record['notices'].append(name)
        self.fixture.record['files'][name] = candidate.digest(b'fixture')
        self.fixture.save()
        self.refuse()

    def test_reader_uses_same_bytes_for_digest_and_header(self):
        original = candidate.mac.read_regular
        producer = self.root / self.fixture.images['producer']
        reads = 0
        def read(path, limit):
            nonlocal reads
            value = original(path, limit)
            if path == producer:
                reads += 1
                producer.write_bytes(b'changed after read')
            return value
        with mock.patch.object(candidate.mac, 'read_regular', side_effect=read):
            self.assertTrue(self.fixture.verify()['materialsMatched'])
        self.assertEqual(reads, 1)
        # No consumer executes the changed path; the report describes observed bytes.


if __name__ == '__main__':
    unittest.main()
