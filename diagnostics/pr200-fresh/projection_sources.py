"""Strict test-only access to separately pinned projection baselines."""
import os
from pathlib import Path
import subprocess

PINS = {
    'source': ('OWNED_TEST_SOURCE', 'c6c5ae5342fcde6b1bf77eff744ad48ddce5b8da',
               'e278496a55b4accc6caafa7a7a99538e5c8ceeca',
               'ui/tauri/native-smoke/supervise.ps1'),
    'contract': ('OWNED_TEST_CONTRACT', '613f4ff550cd65334b00b262a0beebbc3e069fad',
                 '627bdb664be8f3a9b807077466c7473e915387b2',
                 'diagnostics/pr200-fresh/phase2.py'),
}


def git(root, *args):
    result = subprocess.run(['git', '-C', str(root), *args], capture_output=True, timeout=20)
    if result.returncode:
        raise ValueError('FIXED_PROJECTION_SOURCE_REJECTED')
    return result.stdout


def fixed_blob(kind):
    variable, commit, tree, relative = PINS[kind]
    value = os.environ.get(variable)
    if not value or not Path(value).is_absolute():
        raise ValueError('FIXED_PROJECTION_SOURCE_REJECTED')
    root = Path(value).resolve(strict=True)
    if (Path(git(root, 'rev-parse', '--show-toplevel').decode().strip()).resolve() != root
            or git(root, 'rev-parse', 'HEAD').decode().strip() != commit
            or git(root, 'rev-parse', 'HEAD^{tree}').decode().strip() != tree
            or git(root, 'status', '--porcelain', '--untracked-files=all').strip()):
        raise ValueError('FIXED_PROJECTION_SOURCE_REJECTED')
    original = git(root, 'show', 'HEAD:' + relative)
    actual = (root / relative).read_bytes()
    if actual not in (original, original.replace(b'\n', b'\r\n')):
        raise ValueError('FIXED_PROJECTION_SOURCE_REJECTED')
    return original


def control_blob(root):
    """Validate exact working representation, then project the pinned Git bytes."""
    import re
    expected = os.environ.get('EXPECTED_CONTROL', '')
    if not re.fullmatch('[0-9a-f]{40}', expected) or not root.is_absolute():
        raise ValueError('FIXED_PROJECTION_SOURCE_REJECTED')
    root = root.resolve(strict=True)
    tree = git(root, 'rev-parse', expected + '^{tree}')
    if (Path(git(root, 'rev-parse', '--show-toplevel').decode().strip()).resolve() != root
            or git(root, 'rev-parse', 'HEAD').decode().strip() != expected
            or git(root, 'rev-parse', 'HEAD^{tree}') != tree
            or git(root, 'status', '--porcelain', '--untracked-files=all').strip()):
        raise ValueError('FIXED_PROJECTION_SOURCE_REJECTED')
    relative = 'diagnostics/pr200-fresh/phase2.py'
    original = git(root, 'show', expected + ':' + relative)
    actual = (root / relative).read_bytes()
    if b'\r' in original or actual not in (original, original.replace(b'\n', b'\r\n')):
        raise ValueError('FIXED_PROJECTION_SOURCE_REJECTED')
    return original
