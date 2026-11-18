"""Check token-validation wiring through the development CLI, not native UI."""

import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class TokenCommandTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='rescue-token-check-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        scripts = Path(__file__).resolve().parents[1]
        (self.root / 'scripts').mkdir()
        shutil.copy2(scripts / 'dev.py', self.root / 'scripts/dev.py')
        self.design = self.root / 'ui/tauri/design'
        self.design.mkdir(parents=True)
        for name in ('check_tokens.py', 'tokens.css', 'components.css', 'preview.css'):
            shutil.copy2(scripts.parent / 'ui/tauri/design' / name, self.design / name)
        self.palettes = []
        for relative in ('ui/mac-status/Sources/StatusUI/TetherPalette.swift',
                         'ui/mac-development/Sources/TetherDevelopment/TetherPalette.swift'):
            target = self.root / relative
            target.parent.mkdir(parents=True)
            shutil.copy2(scripts.parent / relative, target)
            self.palettes.append(target)

    def run_tokens(self, **environment):
        return subprocess.run(
            [sys.executable, str(self.root / 'scripts/dev.py'), 'tokens'],
            cwd=self.root.parent,
            env=dict(os.environ, **environment),
            capture_output=True,
            text=True,
            timeout=30,
        )

    def test_published_tokens_pass_from_another_directory(self):
        result = self.run_tokens()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('PASS:', result.stdout)
        self.assertIn('contrast pairs. Light theme only.', result.stdout)

    def test_unknown_reference_is_rejected_even_with_optimization_environment(self):
        with (self.design / 'components.css').open('a', encoding='utf-8') as css:
            css.write('\n.probe { color: var(--sys-probe-missing); }\n')
        result = self.run_tokens(PYTHONOPTIMIZE='2')
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('Unknown token: --sys-probe-missing', result.stderr)

    def test_white_text_on_white_surface_is_rejected(self):
        path = self.design / 'tokens.css'
        source = path.read_text(encoding='utf-8')
        original = '--sys-text-primary: var(--ref-ink);'
        self.assertIn(original, source)
        path.write_text(
            source.replace(original, '--sys-text-primary: var(--ref-white);', 1),
            encoding='utf-8',
        )
        result = self.run_tokens()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('Contrast below target:', result.stderr)
        self.assertIn("('text-primary', 'surface-default', 1.0)", result.stderr)

    def mutate(self, path, original, replacement):
        source = path.read_text(encoding='utf-8')
        self.assertIn(original, source)
        path.write_text(source.replace(original, replacement, 1), encoding='utf-8')

    def test_primary_green_reused_as_a_state_color_is_rejected(self):
        # 操作の色（緑）を「終了確認済」の状態色に使うと、操作と正常が同じ色になる。
        self.mutate(self.design / 'tokens.css', '--sys-confirmation-fg: var(--ref-purple-700);',
                    '--sys-confirmation-fg: var(--ref-green-700);')
        result = self.run_tokens()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('Primary action color is shared with a state role', result.stderr)

    def test_state_color_close_to_primary_hue_is_rejected(self):
        # 値が違っても、緑に近い色相を状態色にすれば区別できない（旧案の teal #007F60 の再発防止）。
        self.mutate(self.design / 'tokens.css', '--ref-purple-700: #6e5c83;', '--ref-purple-700: #007f60;')
        result = self.run_tokens()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('Primary action color is too close to a state role', result.stderr)

    def test_mac_palette_drifting_from_tokens_is_rejected(self):
        # 値が AA を満たしても、Mac だけ別の色になれば Tauri と見た目がずれる。
        self.mutate(self.palettes[0], 'static let ink = 0x33413D', 'static let ink = 0x33413E')
        self.mutate(self.palettes[1], 'static let ink = 0x33413D', 'static let ink = 0x33413E')
        result = self.run_tokens()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('Mac palette differs from tokens.css: ink', result.stderr)

    def test_two_mac_apps_must_share_one_palette(self):
        self.mutate(self.palettes[1], 'static let line = 0xDFE9E5', 'static let line = 0xDFE9E6')
        result = self.run_tokens()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('Mac palette differs between mac-status and mac-development', result.stderr)

    def test_contrast_table_in_docs_is_the_checker_output(self):
        # docs の一覧は検査の出力そのもの。値を変えたのに docs が古いままにならない。
        repository = Path(__file__).resolve().parents[2]
        table = subprocess.run([sys.executable, str(repository / 'ui/tauri/design/check_tokens.py'), '--markdown'],
                               capture_output=True, text=True, timeout=30, check=True).stdout.strip()
        docs = (repository / 'docs/tauri-design-tokens.md').read_text(encoding='utf-8')
        block = docs.split('<!-- contrast-table:start -->', 1)[1].split('<!-- contrast-table:end -->', 1)[0]
        self.assertEqual(block.strip(), table)


if __name__ == '__main__':
    unittest.main()
