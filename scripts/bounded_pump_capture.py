"""Control-only admission and finite evidence; the pinned source publisher is unchanged."""
import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

SOURCE = 'cee2e9baa22200c929f015199aac68626925a486'
TREE = 'ce3c61a0e56b2bec1fdc2a5990813dff17afb4ec'
EXPECTED_TESTS = 41
QUALIFICATION = {'schema': 1, 'qualification': 'windows-x64-pump-adapter-last-error',
                 'testsRun': EXPECTED_TESTS, 'lastErrorCaseExecuted': True,
                 'firstUseTlsAndOriginalErrorConversion': True,
                 'guardRemovalNegativeControl': True, 'nativeLaunches': 0, 'passed': True}


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode('ascii')


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def load(path):
    info = path.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or
            getattr(info, 'st_file_attributes', 0) & 1024 or info.st_size > 2048):
        raise ValueError('REJECTED')
    data = path.read_bytes()
    if len(data) > 2048: raise ValueError('REJECTED')
    value = json.loads(data)
    # Canonical equality also rejects duplicate keys, BOM and trailing whitespace.
    if canonical(value) != data: raise ValueError('REJECTED')
    return value


def write_new(path, value):
    with path.open('xb') as stream:
        stream.write(canonical(value))


def binding():
    control = os.environ.get('CONTROL_HEAD', '')
    ordinal = os.environ.get('TRIAL_ORDINAL', '')
    if (not re.fullmatch('[0-9a-f]{40}', control) or ordinal not in ('1', '2', '3')
            or os.environ.get('WRY_PIN') != SOURCE or os.environ.get('WRY_TREE') != TREE):
        raise ValueError('REJECTED')
    return {'controlCommit': control, 'sourceCommit': SOURCE, 'sourceTree': TREE,
            'trialOrdinal': int(ordinal)}


def qualification(value):
    expected = {**QUALIFICATION, **binding()}
    # Canonical byte equality distinguishes bool from int and fixes every key/value.
    if canonical(value) != canonical(expected): raise ValueError('REJECTED')
    return expected


def emit(kind, value):
    print('WRY_' + kind + '_BEGIN')
    print(canonical(value).decode('ascii'))
    print('WRY_' + kind + '_SHA256=' + digest(value))
    print('WRY_' + kind + '_END')


def receipt(temp):
    value = qualification(load(temp / 'qualification.json'))
    expected = {'qualificationSha256': digest(value), **binding()}
    if canonical(load(temp / 'qualification-published.json')) != canonical(expected):
        raise ValueError('REJECTED')
    return expected


def admit(temp, source):
    proof = receipt(temp)
    steps = source.safe_steps(source.load(temp / 'steps.json', 4096))
    expected = [{'stage': name, 'exitCode': 0} for name in source.STEPS[:12]]
    if steps != expected: raise ValueError('REJECTED')
    write_new(temp / 'qualification-admitted.json', proof)


def source_module(root):
    # Dependencies and the publisher are loaded from the immutable source checkout.
    sys.path.insert(0, str(root / 'scripts'))
    spec = importlib.util.spec_from_file_location('fixed_source_publisher', root / 'scripts/wry_probe_ci.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def decision(body, passed, ordinal):
    if type(ordinal) is not int or ordinal not in (1, 2, 3): raise ValueError('REJECTED')
    if (passed is not True or body.get('allOriginalAndDiagnosticChecksPassed') is not True
            or body.get('sourceAndSpecimenStable') is not True
            or any(body.get(k) is not True for k in ('deliveryClassificationComplete',
                   'receiverClassificationComplete', 'pumpClassificationComplete'))):
        return 'stop'
    return 'healthy-' + str(ordinal) if ordinal < 3 else 'inconclusive-three-healthy'


def finish(root, temp, source):
    # Capture only the unchanged source publisher's finite safe projection, not raw logs.
    output = io.StringIO()
    passed = False
    try:
        with contextlib.redirect_stdout(output):
            passed = source.publish(root, temp, delivery=True)
    except Exception:
        # A completed safe record may precede a summary-write failure. Retain it
        # only through the same bounded canonical validation below; never continue.
        passed = False
    text = output.getvalue()
    lines = text.splitlines()
    if (len(text.encode()) > 34000 or len(lines) != 4 or lines[0] != 'WRY_DIAGNOSTIC_RECORD_BEGIN'
            or lines[3] != 'WRY_DIAGNOSTIC_RECORD_END'):
        raise ValueError('REJECTED')
    body = json.loads(lines[1])
    if canonical(body).decode() != lines[1] or lines[2] != 'WRY_DIAGNOSTIC_RECORD_SHA256=' + digest(body):
        raise ValueError('REJECTED')
    # Preserve the original native fail and bounded partial record even on rejection.
    print(text, end='')
    bind = binding()
    status = 'stop'
    try:
        proof = receipt(temp)
        if canonical(load(temp / 'qualification-admitted.json')) != canonical(proof): raise ValueError('REJECTED')
        if any(body.get(k) != v for k, v in bind.items() if k != 'trialOrdinal'): raise ValueError('REJECTED')
        status = decision(body, passed, bind['trialOrdinal'])
    except (OSError, ValueError, TypeError, KeyError):
        pass
    value = {'schema': 1, **bind, 'outcome': status, 'diagnosticSha256': digest(body),
             'rootCauseEstablished': False, 'timeoutFixed': False}
    emit('BOUNDED_TRIAL', value)
    if status == 'stop': return 1
    # Only two exact tokens permit continuation. Trial 3 never emits one.
    if status in ('healthy-1', 'healthy-2'):
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as stream:
            stream.write('continuation=' + status + '\n')
    return 0


class ClosedParser(argparse.ArgumentParser):
    def error(self, message):
        raise ValueError("REJECTED")


def main(argv=None):
    parser = ClosedParser(add_help=False)
    parser.add_argument('mode', choices=('qualify', 'qualification', 'admit', 'finish'))
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--temp', type=Path, required=True)
    try:
        args = parser.parse_args(argv)
        binding()
        if args.mode == 'qualify':
            from qualify_wry_pump_windows import qualify
            value = qualification({**qualify(args.root), **binding()})
            write_new(args.temp / 'qualification.json', value)
        elif args.mode == 'qualification':
            value = qualification(load(args.temp / 'qualification.json'))
            emit('PUMP_QUALIFICATION', value)
            write_new(args.temp / 'qualification-published.json',
                      {'qualificationSha256': digest(value), **binding()})
        else:
            source = source_module(args.root)
            if args.mode == 'admit': admit(args.temp, source)
            else: return finish(args.root, args.temp, source)
        return 0
    except (OSError, ValueError, TypeError, KeyError, ImportError, RecursionError, subprocess.SubprocessError):
        print('{"code":"BOUNDED_CAPTURE_REJECTED"}')
        return 1


if __name__ == '__main__': raise SystemExit(main())
