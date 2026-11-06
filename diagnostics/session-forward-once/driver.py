"""Private stage-2 driver preparation. No native process is launched here."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import overlay
import pins
from probe_schema import VARIANT, unique


def module(name, file):
    spec = importlib.util.spec_from_file_location(name, file)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def images(root, archive):
    scope = root / 'diagnostic-scope'
    base = module('fixed_driver_patch', scope / 'scripts/apply_driver_trace.py')
    before = base.upstream_files(archive.read_bytes())
    patch, code, expected = base.bundle(scope / 'tools/native-driver-trace')
    stage1 = base.apply_exact(before, patch)
    stage1.update({'src/' + name: data for name, data in code.items()})
    overlay.need(all(base.sha(stage1[n]) == h for n, h in expected.items()))
    folder = scope / 'diagnostics/session-forward'
    stage2 = base.apply_exact(stage1, (folder / 'connection.patch').read_bytes())
    expected2 = json.loads((folder / 'postimage.json').read_text())
    overlay.need(set(expected2) == {'src/server.rs', 'src/diagnostic.rs'})
    overlay.need(all(base.sha(stage2[n]) == h for n, h in expected2.items()))
    overlay.need(base.sha(before['Cargo.lock']) == pins.LOCK)
    return base, before, stage1, stage2, patch


def quiet(root, name, args, expected=None, cwd=None, env=None):
    log = root / 'private' / (name + '.log')
    with log.open('wb') as stream:
        result = subprocess.run(args, cwd=cwd, env=env, stdout=stream, stderr=subprocess.STDOUT,
                                timeout=240, check=False)
    overlay.need(result.returncode == 0)
    if expected:
        text = log.read_text(encoding='utf-8', errors='replace')
        overlay.need(re.search(expected, text) is not None)


def preflight(root, archive):
    record = root / 'safe/transport-preflight.json'
    overlay.need(not record.exists())
    facts = {'schema': 1, 'fixedPython': 'not-run', 'realHandler': 'not-run',
             'fixedPythonPassed': 0, 'realHandlerAndRecorderPassed': 0, 'nativeTrials': 0}
    def save():
        record.write_text(json.dumps(facts, sort_keys=True) + '\n')
    save()
    scope = root / 'diagnostic-scope'
    prepare = module('fixed_forward_fixture', scope / 'diagnostics/session-forward/prepare_fixture.py')
    prepare.prepare(archive, root / 'transport-fixture')
    env = dict(os.environ)
    env['SESSION_FORWARD_ARCHIVE'] = str(archive)
    rust_env = dict(os.environ)
    rust_env['CARGO_TERM_COLOR'] = 'never'
    calls = [
        ('fixedPython', 'fixed-source-python', [sys.executable, '-B', '-m', 'unittest', 'discover', '-s',
         str(scope / 'diagnostics/session-forward'), '-p', 'test_source.py', '-v'], r'Ran 5 tests[\s\S]*\nOK\s*$', env, 'fixedPythonPassed', 5),
        ('realHandler', 'real-handler-fixture', ['cargo', '+1.99.1', 'test', '--locked', '--manifest-path',
         str(root / 'transport-fixture/Cargo.toml'), '--target-dir', str(root / 'transport-fixture-target'),
         '--lib', '--', '--test-threads=1', '--color', 'never'],
         r'test result: ok\. 10 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out', rust_env, 'realHandlerAndRecorderPassed', 10)]
    for key, label, args, pattern, child_env, passed_key, count in calls:
        facts[key] = 'running'
        save()
        try:
            quiet(root, label, args, pattern, env=child_env)
            facts[key] = 'passed'
            facts[passed_key] = count
        except Exception as error:
            facts[key] = 'timeout' if isinstance(error, subprocess.TimeoutExpired) else 'failed'
            save()
            raise
        save()


def provenance(root, archive, stage2, patch, binary=None):
    scope = root / 'diagnostic-scope'
    names = ['src/main.rs', 'src/server.rs', 'src/diagnostic.rs', 'src/diagnostic_tests.rs']
    return {'schema': 1, 'variant': VARIANT, 'diagnosticSource': pins.DIAGNOSTIC,
        'upstreamSha256': overlay.sha(archive.read_bytes()), 'basePatchSha256': overlay.sha(patch),
        'connectionPatchSha256': overlay.sha((scope / 'diagnostics/session-forward/connection.patch').read_bytes()),
        'cargoLockSha256': pins.LOCK, 'postimage': {n: overlay.sha(stage2[n]) for n in names},
        'sourceFileCount': len(stage2), 'binarySha256': overlay.sha(binary.read_bytes()) if binary else None}


def perform(stage, root, source, archive, binary=None):
    overlay.need(archive.resolve() == (root / 'tools/tauri-driver.crate').resolve())
    overlay.need(source.resolve() == (root / 'tools/tauri-driver-2.1.0').resolve())
    base, before, stage1, stage2, patch = images(root, archive)
    if stage == 'apply':
        base.check_tree(source, stage1)
        preflight(root, archive)
        for name in ('src/server.rs', 'src/diagnostic.rs'):
            (source / name).write_bytes(stage2[name])
    base.check_tree(source, stage2)
    if stage == 'verify':
        overlay.need(binary and binary.resolve() == (root / 'tools/tauri/bin/tauri-driver.exe').resolve())
        overlay.need(binary.is_file() and not binary.is_symlink())
    value = provenance(root, archive, stage2, patch, binary)
    if binary:
        previous = json.loads((root / 'tools/driver-provenance.json').read_text(), object_pairs_hook=unique)
        saved = root / 'safe/driver-provenance.json'
        if saved.exists():
            overlay.need(previous == value and json.loads(saved.read_text(), object_pairs_hook=unique) == value)
        else:
            overlay.need(previous == provenance(root, archive, stage2, patch))
    (root / 'tools/driver-provenance.json').write_text(json.dumps(value, sort_keys=True) + '\n')
    if binary:
        (root / 'safe/driver-provenance.json').write_text(json.dumps(value, sort_keys=True) + '\n')


def main():
    class Parser(argparse.ArgumentParser):
        def error(self, message):
            raise ValueError('FORWARD_DRIVER_ARGUMENT_REJECTED')
    parser = Parser()
    parser.add_argument('stage', choices=['apply', 'verify'])
    for name in ('root', 'source', 'archive'):
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--binary', type=Path)
    try:
        args = parser.parse_args()
        perform(args.stage, args.root.resolve(), args.source.resolve(), args.archive.resolve(), args.binary)
    except Exception:
        print('FORWARD_DRIVER_STAGE_REJECTED')
        return 1
    print('FORWARD_DRIVER_STAGE_VERIFIED')
    return 0


if __name__ == '__main__':
    sys.exit(main())
