"""Exactly one fixed-source parser/ledger/detail observation. No app/tool/native path."""
import argparse
import contextlib
import hashlib
import importlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys

SOURCE = '43f56b87574f57cc87c1d0347b609499d896c222'
TREE = 'e78c82786c440f76414ab55939efb5fa7efbbe50'
PATCH = 'f692839091fec12a079041213edcc1ad9e6de0b7dc3f23557c26a31d0ca7a5c5'
FILES = {'observer': 'observe-profile.ps1', 'locationMapper': 'observer-detail.ps1',
         'ledgerFixture': 'observer-reasons.test.ps1', 'detailFixture': 'observer-detail.test.ps1',
         'parser': 'parse-predicate-sources.ps1', 'auditSourceParsedOnly': 'audit.ps1',
         'preflightRecorder': 'powershell_preflight.py', 'schemaHelpers': 'report.py'}
OUTCOMES = {'boundary-rejected', 'recorder-rejected', 'fixture-failed', 'fixture-passed'}


def need(value):
    if not value:
        raise ValueError('FIXTURE_ONLY_BOUNDARY_REJECTED')


def git(root, *args):
    return subprocess.run(['git', '-C', str(root), *args], check=True, capture_output=True,
                          timeout=5).stdout


def sha(value):
    return hashlib.sha256(value).hexdigest()


def source_hashes(source):
    need(git(source, 'rev-parse', 'HEAD').decode().strip() == SOURCE)
    need(git(source, 'rev-parse', 'HEAD^{tree}').decode().strip() == TREE)
    need(not git(source, 'status', '--porcelain', '--untracked-files=all').strip())
    result = {}
    for label, name in FILES.items():
        original = git(source, 'show', SOURCE + ':diagnostics/pr200-fresh/' + name)
        actual = (source / 'diagnostics/pr200-fresh' / name).read_bytes()
        need(actual in (original, original.replace(b'\n', b'\r\n')))
        result[label] = sha(actual)
    return result


def load_preflight(source):
    folder = (source / 'diagnostics/pr200-fresh').resolve()
    # A fresh recorder process imports only these fixed-source helper definitions.
    need('powershell_preflight' not in sys.modules and 'report' not in sys.modules)
    sys.path.insert(0, str(folder))
    module = importlib.import_module('powershell_preflight')
    need(Path(module.__file__).resolve().parent == folder)
    need(Path(sys.modules['report'].__file__).resolve().parent == folder)
    return module


def identity_safe(value):
    need(isinstance(value, dict) and set(value) == {'schema', 'controlCommit', 'controlTree',
        'fixtureSourceCommit', 'fixtureSourceTree', 'reviewedPatchSha256', 'sourceVerified',
        'sourceStableAfter', 'sourceFileSha256', 'runnerLabel', 'pythonVersion', 'powershellVersion'})
    need(type(value['schema']) is int and value['schema'] == 1)
    for key in ('controlCommit', 'controlTree'):
        need(value[key] is None or isinstance(value[key], str) and re.fullmatch('[0-9a-f]{40}', value[key]))
    need(value['fixtureSourceCommit'] == SOURCE and value['fixtureSourceTree'] == TREE and value['reviewedPatchSha256'] == PATCH)
    need(type(value['sourceVerified']) is bool and (value['sourceStableAfter'] is None or type(value['sourceStableAfter']) is bool))
    hashes = value['sourceFileSha256']
    need(isinstance(hashes, dict) and set(hashes) <= set(FILES))
    need(all(isinstance(item, str) and re.fullmatch('[0-9a-f]{64}', item) for item in hashes.values()))
    need(not value['sourceVerified'] or set(hashes) == set(FILES))
    need(value['sourceStableAfter'] is None or value['sourceVerified'])
    need(value['runnerLabel'] == 'windows-2022')
    need(value['pythonVersion'] is None or value['pythonVersion'] == '3.13.17')
    need(value['powershellVersion'] is None or isinstance(value['powershellVersion'], str) and
         re.fullmatch(r'[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,5})?', value['powershellVersion']))
    return value


def publish(identity, preflight, outcome, validator):
    need(outcome in OUTCOMES)
    files = {'identity.json': identity_safe(identity)}
    if preflight is not None:
        need(validator is not None)
        files['powershell-preflight.json'] = validator(preflight)
        need(preflight['schema'] == 3)
    if outcome in {'fixture-passed', 'fixture-failed'}:
        need(preflight is not None and identity['sourceVerified'] and identity['sourceStableAfter'] is True)
        need(identity['controlCommit'] is not None and identity['controlTree'] is not None)
        need(identity['pythonVersion'] is not None and identity['powershellVersion'] is not None)
        need(preflight['passed'] is (outcome == 'fixture-passed'))
    canonical = lambda value: json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
    record = {'schema': 1, 'fixtureOnly': True, 'observationCap': 1, 'pr200Blocked': True,
              'outcome': outcome, 'files': files, 'fileSha256': {key: sha(canonical(value)) for key, value in files.items()}}
    raw = canonical(record)
    need(len(raw) <= 30000)
    body = 'BEGIN_PR213_FIXTURE_RECORD sha256:' + sha(raw) + '\n' + json.dumps(record, sort_keys=True, indent=2) + '\nEND_PR213_FIXTURE_RECORD\n'
    need(len(body.encode()) <= 60000 and max(map(len, body.splitlines())) <= 2000)
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a', encoding='utf-8') as summary:
        summary.write('```json\n' + body + '```\n')
    print(body, end='', flush=True)


def observe(source, control, root):
    identity = {'schema': 1, 'controlCommit': None, 'controlTree': None,
        'fixtureSourceCommit': SOURCE, 'fixtureSourceTree': TREE, 'reviewedPatchSha256': PATCH,
        'sourceVerified': False, 'sourceStableAfter': None, 'sourceFileSha256': {},
        'runnerLabel': 'windows-2022', 'pythonVersion': None, 'powershellVersion': None}
    outcome, data, module = 'boundary-rejected', None, None
    try:
        need(sys.platform == 'win32' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted' and
             os.environ.get('RUNNER_OS') == 'Windows' and os.environ.get('RUNNER_ARCH') == 'X64')
        need(os.environ.get('RECORDER_PS_PATH_MATCH') == 'true')
        version = '.'.join(map(str, sys.version_info[:3]))
        need(version == '3.13.17')
        identity['pythonVersion'] = version
        ps_version = os.environ.get('RECORDER_POWERSHELL_VERSION', '')
        need(re.fullmatch(r'[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,5})?', ps_version))
        identity['powershellVersion'] = ps_version
        expected = os.environ.get('EXPECTED_CONTROL', '')
        need(re.fullmatch('[0-9a-f]{40}', expected))
        commit = git(control, 'rev-parse', 'HEAD').decode().strip()
        tree = git(control, 'rev-parse', 'HEAD^{tree}').decode().strip()
        need(commit == expected and re.fullmatch('[0-9a-f]{40}', tree))
        need(not git(control, 'status', '--porcelain', '--untracked-files=all').strip())
        identity.update(controlCommit=commit, controlTree=tree)
        identity['sourceFileSha256'] = source_hashes(source)
        identity['sourceVerified'] = True
        module = load_preflight(source)
        root.mkdir(exist_ok=False)
        outcome = 'recorder-rejected'
        with contextlib.redirect_stdout(io.StringIO()):
            code = module.preflight(source, root)  # Exactly once; it preserves fail-closed stage order.
        data = module.preflight_safe(json.loads((root / 'safe/powershell-preflight.json').read_text(encoding='utf-8')))
        need(data['schema'] == 3 and code in {0, 1} and data['passed'] is (code == 0))
        identity['sourceStableAfter'] = source_hashes(source) == identity['sourceFileSha256']
        need(identity['sourceStableAfter'])
        outcome = 'fixture-passed' if code == 0 else 'fixture-failed'
    except Exception:
        # Preserve an already written partial report without invoking any stage again.
        if module is not None and data is None:
            try:
                data = module.preflight_safe(json.loads((root / 'safe/powershell-preflight.json').read_text(encoding='utf-8')))
            except Exception:
                pass
        # No exception type/text, raw subprocess output or unvalidated value is copied.
    publish(identity, data, outcome, module.preflight_safe if module else None)
    return 0 if outcome == 'fixture-passed' else 1


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--control', type=Path, required=True)
    parser.add_argument('--root', type=Path, required=True)
    args = parser.parse_args()
    try:
        return observe(args.source.resolve(), args.control.resolve(), args.root.resolve())
    except Exception:
        print('FIXTURE_ONLY_RECORDING_REJECTED')
        return 1


if __name__ == '__main__':
    sys.exit(main())
