"""Fixed prior-red source qualification and single-invocation admission only."""
import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest

SOURCE = '008073aa952959f2a4937e31fe10de6609c1627a'
TREE = '4dcfba754be4a150a28d1dfb770c2ea65e3fcaa4'
SECTIONS = {'steps', 'native', 'observation', 'wry', 'delivery', 'receiver',
            'driver', 'host', 'launch', 'final', 'overlay', 'build'}
TESTS = 31


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode('ascii')


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], stderr=subprocess.DEVNULL).strip().decode()


def binding(root):
    control = git(Path(__file__).resolve().parents[1], 'rev-parse', 'HEAD')
    if (os.environ.get('CONTROL_HEAD') != control or os.environ.get('WRY_PIN') != SOURCE
            or os.environ.get('WRY_TREE') != TREE or git(root, 'rev-parse', 'HEAD') != SOURCE
            or git(root, 'rev-parse', 'HEAD^{tree}') != TREE):
        raise ValueError('PIN_REJECTED')
    return {'controlCommit': control, 'sourceCommit': SOURCE, 'sourceTree': TREE}


def source_module(root):
    sys.path.insert(0, str(root / 'scripts'))
    spec = importlib.util.spec_from_file_location('prior_red_source', root / 'scripts/wry_probe_ci.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def expected(bind):
    return {'schema': 1, 'qualification': 'prior-red-source-fixtures', **bind,
            'discovered': TESTS, 'started': TESTS, 'passed': TESTS, 'skipped': 0,
            'failures': 0, 'errors': 0, 'expectedFailures': 0, 'unexpectedSuccesses': 0,
            'nativeLaunches': 0}


class ExecutedResult(unittest.TextTestResult):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.started = []
        self.passed = []

    def startTest(self, test):
        self.started.append(test.id())
        super().startTest(test)

    def addSuccess(self, test):
        self.passed.append(test.id())
        super().addSuccess(test)


def cases(suite):
    for item in suite:
        if isinstance(item, unittest.TestSuite): yield from cases(item)
        else: yield item


def qualify(root, bind):
    suite = unittest.defaultTestLoader.discover(str(root / 'scripts/tests'), pattern='test_wry*.py')
    ids = sorted(case.id() for case in cases(suite))
    result = unittest.TextTestRunner(verbosity=2, resultclass=ExecutedResult).run(suite)
    if (len(ids) != TESTS or len(set(ids)) != TESTS or sorted(result.started) != ids
            or sorted(result.passed) != ids or result.testsRun != TESTS
            or result.skipped or result.failures or result.errors or result.expectedFailures
            or result.unexpectedSuccesses):
        raise ValueError('QUALIFICATION_REJECTED')
    return expected(bind)


def write_new(path, value):
    with path.open('xb') as stream: stream.write(canonical(value))


def receipt(temp, source, bind):
    value = source.load(temp / 'prior-red-qualification.json', 2048)
    if canonical(value) != canonical(expected(bind)): raise ValueError('QUALIFICATION_REJECTED')
    return value


def emit(value):
    print('WRY_PRIOR_RED_QUALIFICATION_BEGIN')
    print(canonical(value).decode())
    print('WRY_PRIOR_RED_QUALIFICATION_SHA256=' + digest(value))
    print('WRY_PRIOR_RED_QUALIFICATION_END', flush=True)


def publish_qualification(temp, source, bind):
    value = receipt(temp, source, bind)
    emit(value)
    write_new(temp / 'prior-red-qualification-published.json', {'qualificationSha256': digest(value), **bind})


def admit(root, temp, source, bind):
    value = receipt(temp, source, bind)
    published = source.load(temp / 'prior-red-qualification-published.json', 2048)
    if canonical(published) != canonical({'qualificationSha256': digest(value), **bind}):
        raise ValueError('QUALIFICATION_PUBLICATION_REJECTED')
    steps = source.safe_steps(source.load(temp / 'steps.json', 4096))
    if canonical(steps) != canonical([{'stage': name, 'exitCode': 0} for name in source.STEPS[:12]]):
        raise ValueError('SETUP_REJECTED')
    source.permissions(root, True)
    identity = source.source_identity(root, temp / 'wry', temp / 'wry.crate', True)
    for relative in ('rescue-host-shell.exe', 'BUILD.json'):
        if (temp / 'unpacked' / relative).read_bytes() != (temp / 'fixture' / relative).read_bytes():
            raise ValueError('SPECIMEN_REJECTED')
    paths = {'exeSha256': 'unpacked/rescue-host-shell.exe', 'driverSha256': 'tools/tauri/bin/tauri-driver.exe',
             'archiveSha256': 'fixture.zip', 'edgeDriverSha256': 'tools/edge/msedgedriver.exe',
             'driverLockSha256': 'tools/tauri-driver-2.1.0/Cargo.lock'}
    identity.update({key: source.digest((temp / path).read_bytes()) for key, path in paths.items()})
    identity['driverPatchSha256'] = source.digest((root / 'tools/native-driver-trace/driver.patch').read_bytes())
    manifest = {'schema': 1, 'variant': 'host-startup-diagnostic-1', 'sourceCommit': SOURCE,
                'features': ['custom-protocol', 'startup-diagnostics'], 'profile': 'release',
                'exeSha256': identity['exeSha256'], 'distribution': False}
    if canonical(source.load(temp / 'unpacked/BUILD.json', 2048)) != canonical(manifest):
        raise ValueError('SPECIMEN_REJECTED')
    if (identity.get('sourceCommit') != SOURCE or identity.get('sourceTree') != TREE
            or canonical(identity) != canonical(source.safe_meta(source.load(temp / 'launch.json', 4096), delivery=True))):
        raise ValueError('IDENTITY_REJECTED')
    # Disposable run-local duplicate guard, not a persistent exactly-once ledger.
    write_new(temp / 'prior-red-admitted.json', {'qualificationSha256': digest(value), **bind})


def validate_record(text, bind):
    lines = text.splitlines()
    if (len(text.encode()) > 34000 or len(lines) != 4 or lines[0] != 'WRY_DIAGNOSTIC_RECORD_BEGIN'
            or lines[3] != 'WRY_DIAGNOSTIC_RECORD_END'): raise ValueError('PUBLICATION_REJECTED')
    body = json.loads(lines[1])
    if (canonical(body).decode() != lines[1] or lines[2] != 'WRY_DIAGNOSTIC_RECORD_SHA256=' + digest(body)
            or set(body['files']) != SECTIONS or any(body.get(k) != v for k, v in bind.items())):
        raise ValueError('PUBLICATION_REJECTED')
    for section in body['files'].values():
        if set(section) != {'data', 'sha256'} or section['sha256'] != digest(section['data']):
            raise ValueError('PUBLICATION_REJECTED')
    return body


def publish(root, temp, source, bind):
    output = io.StringIO()
    passed = False
    try:
        with contextlib.redirect_stdout(output): passed = source.publish(root, temp, delivery=True)
    except Exception:
        pass
    text = output.getvalue()
    validate_record(text, bind)
    print(text, end='', flush=True)
    # Preserve the original verdict. A valid partial/failed record is still printed.
    return 0 if passed is True else 1


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument('mode', choices=('qualify', 'qualification', 'admit', 'publish'))
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--temp', type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        bind = binding(args.root)
        if args.mode in ('qualify', 'admit'):
            subprocess.run(['git', '-C', str(args.root), 'diff', '--exit-code', 'HEAD', '--', '.',
                            ':(exclude)ui/tauri/src-tauri/Cargo.lock'], check=True, capture_output=True)
        if args.mode == 'qualify':
            value = qualify(args.root, bind)
            write_new(args.temp / 'prior-red-qualification.json', value)
        else:
            source = source_module(args.root)
            if args.mode == 'qualification': publish_qualification(args.temp, source, bind)
            elif args.mode == 'admit': admit(args.root, args.temp, source, bind)
            else: return publish(args.root, args.temp, source, bind)
        return 0
    except (OSError, ValueError, TypeError, KeyError, ImportError, subprocess.SubprocessError):
        print('{"code":"PRIOR_RED_RECOVERY_REJECTED"}')
        return 1


if __name__ == '__main__': raise SystemExit(main())
