"""Offline real three-checkout topology; control genuinely lacks old objects."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import projection_sources as sources

ROOT = Path(__file__).resolve().parents[2]


def command(*args):
    return sources.git(args[0], *args[1:])


class ProjectionSourcesTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Both baselines must first pass the actual strict resolver; no broad fetch.
        cls.blobs = {key: sources.fixed_blob(key) for key in sources.PINS}
        cls.temp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.temp.cleanup)
        cls.root = Path(cls.temp.name)
        cls.seed = cls.root / 'seed.git'
        result = subprocess.run(['git', 'init', '--bare', str(cls.seed)], capture_output=True)
        if result.returncode: raise ValueError('SHALLOW_FIXTURE_REJECTED')
        refs = {'control': (ROOT, command(ROOT, 'rev-parse', 'HEAD').decode().strip())}
        refs.update({key: (Path(os.environ[pin[0]]), pin[1]) for key, pin in sources.PINS.items()})
        for key, (repository, commit) in refs.items():
            command(cls.seed, 'fetch', '--no-tags', '--depth=1', str(repository),
                    commit + ':refs/heads/' + key)
            result = subprocess.run(['git', 'clone', '--no-local', '--no-hardlinks',
                                     '--depth=1', '--single-branch', '--branch', key,
                                     cls.seed.resolve().as_uri(), str(cls.root / key)], capture_output=True)
            if result.returncode: raise ValueError('SHALLOW_FIXTURE_REJECTED')
        cls.environment = {pin[0]: str(cls.root / key) for key, pin in sources.PINS.items()}

    def test_old_control_lookup_is_red_and_corrected_projection_is_green(self):
        import owned_control
        import owned_overlay
        control = self.root / 'control'
        self.assertEqual(command(control, 'rev-parse', '--is-shallow-repository').strip(), b'true')
        for key, pin in sources.PINS.items():
            checkout = self.root / key
            self.assertEqual(command(checkout, 'rev-parse', '--is-shallow-repository').strip(), b'true')
            self.assertNotEqual((checkout / '.git').resolve(), (control / '.git').resolve())
            old = subprocess.run(['git', '-C', str(control), 'show', pin[1] + ':' + pin[3]], capture_output=True)
            self.assertEqual(old.returncode, 128)  # Actual PR225 failure boundary.
            absent = subprocess.run(['git', '-C', str(control), 'cat-file', '-e', pin[1] + '^{commit}'], capture_output=True)
            self.assertNotEqual(absent.returncode, 0)
        with patch.dict(os.environ, self.environment):
            original = sources.fixed_blob('source')
            changed = owned_overlay.overlay_bytes(original).decode()
            for block in (owned_overlay.PRELUDE, owned_overlay.WRAP_START, owned_overlay.HOOK, owned_overlay.PERSIST):
                changed = changed.replace(block, '')
            self.assertEqual(changed.encode(), original)
            current = (ROOT / 'diagnostics/pr200-fresh/phase2.py').read_bytes()
            self.assertEqual(owned_control.phase2_projection(current), sources.fixed_blob('contract'))
            self.assertEqual({key: sources.fixed_blob(key) for key in sources.PINS}, self.blobs)

    def test_missing_relative_wrong_and_nested_checkout_targets_reject(self):
        for key, pin in sources.PINS.items():
            other = 'contract' if key == 'source' else 'source'
            values = ('', 'relative', str(self.root / 'missing'), str(self.root / 'control'),
                      str(self.root / other), str(self.root / key / 'ui'))
            for value in values:
                with self.subTest(kind=key, case=values.index(value)):
                    with patch.dict(os.environ, {**self.environment, pin[0]: value}):
                        with self.assertRaises((ValueError, FileNotFoundError)):
                            sources.fixed_blob(key)
            with patch.dict(os.environ, self.environment):
                saved = os.environ.pop(pin[0])
                try:
                    with self.assertRaises(ValueError): sources.fixed_blob(key)
                finally: os.environ[pin[0]] = saved

    def test_wrong_tree_missing_head_and_mutated_checkout_reject(self):
        empty = self.root / 'empty'
        result = subprocess.run(['git', 'init', str(empty)], capture_output=True)
        self.assertEqual(result.returncode, 0)
        for key, pin in sources.PINS.items():
            with patch.dict(os.environ, {**self.environment, pin[0]: str(empty)}):
                with self.assertRaises(ValueError): sources.fixed_blob(key)
            wrong = (pin[0], pin[1], '0' * 40, pin[3])
            with patch.dict(os.environ, self.environment), patch.dict(sources.PINS, {key: wrong}):
                with self.assertRaises(ValueError): sources.fixed_blob(key)
            path = self.root / key / pin[3]
            original = path.read_bytes()
            try:
                path.write_bytes(original + b'\n')
                with patch.dict(os.environ, self.environment):
                    with self.assertRaises(ValueError): sources.fixed_blob(key)
            finally: path.write_bytes(original)
            extra = self.root / key / 'untracked-negative-control'
            try:
                extra.write_bytes(b'synthetic')
                with patch.dict(os.environ, self.environment):
                    with self.assertRaises(ValueError): sources.fixed_blob(key)
            finally: extra.unlink()
            with patch.dict(os.environ, self.environment): self.assertEqual(sources.fixed_blob(key), self.blobs[key])

    def test_control_workflow_explicit_fixed_references_and_one_attempt(self):
        text = (ROOT / '.github/workflows/pr225-owned-reference-once.yml').read_text()
        for required in ("branches: ['diagnostic/pr200-owned-hook-once-20261106']", "types: [opened]",
                         "head.ref == 'diagnostic/pr225-owned-reference-once-20261106'",
                         "base.sha == '5ed7f2f03f3ae026e062c9ff7b2507b53e5606fe'", "github.run_attempt == 1",
                         "github.event.pull_request.draft == true", "runs-on: windows-2022",
                         "OWNED_TEST_SOURCE: ${{ github.workspace }}/source",
                         "OWNED_TEST_CONTRACT: ${{ github.workspace }}/diagnostic", "contents: read"):
            self.assertIn(required, text)
        for pin in sources.PINS.values(): self.assertIn('ref: ' + pin[1], text)
        self.assertEqual(text.count('owned_control.py trial '), 1)
        self.assertEqual(text.count('owned_control.py preflight '), 1)
        self.assertEqual(text.count('cargo +1.99.1 build --release --locked'), 1)
        for prohibited in ('fetch-depth:', 'upload-artifact', 'actions/cache', 'matrix:', 'workflow_dispatch', 'secrets.'):
            self.assertNotIn(prohibited, text)
