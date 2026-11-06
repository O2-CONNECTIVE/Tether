"""Exactly one fixed-source parser/poll fixture observation. No app/tool/native path."""
import argparse
import hashlib
import importlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

SOURCE = '613f4ff550cd65334b00b262a0beebbc3e069fad'
TREE = '627bdb664be8f3a9b807077466c7473e915387b2'
PATCH = 'bae5b0b3ab9a33da43afe79af8b14d94d82cc29e76461e27f77609eb7f9aea1e'
FILES = {'pollFixture': 'diagnostics/pr200-fresh/observer-poll.test.ps1',
         'observer': 'diagnostics/pr200-fresh/observe-profile.ps1',
         'locationMapper': 'diagnostics/pr200-fresh/observer-detail.ps1',
         'ownershipFunctionsParsedOnly': 'ui/tauri/native-smoke/processes.ps1',
         'stageRecorder': 'diagnostics/pr200-fresh/powershell_preflight.py',
         'schemaHelpers': 'diagnostics/pr200-fresh/report.py'}
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
        original = git(source, 'show', SOURCE + ':' + name)
        actual = (source / name).read_bytes()
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
        'sourceStableAfter', 'sourceFileSha256', 'runnerLabel', 'pythonVersion', 'powershellVersion', 'controlParserSha256'})
    need(type(value['schema']) is int and value['schema'] == 1)
    for key in ('controlCommit', 'controlTree'):
        need(value[key] is None or isinstance(value[key], str) and re.fullmatch('[0-9a-f]{40}', value[key]))
    need(value['fixtureSourceCommit'] == SOURCE and value['fixtureSourceTree'] == TREE and value['reviewedPatchSha256'] == PATCH)
    need(type(value['sourceVerified']) is bool and (value['sourceStableAfter'] is None or type(value['sourceStableAfter']) is bool))
    need(value['controlParserSha256'] is None or isinstance(value['controlParserSha256'], str) and re.fullmatch('[0-9a-f]{64}', value['controlParserSha256']))
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


STAGES = ('parse', 'poll')
MARKERS = ('POLL_PARSER_PASS files=4', 'PROFILE_OBSERVER_POLL_SYNTHETIC_PASS cases=6')
FAILURES = {'not-observed', 'not-applicable', 'known', 'missing', 'unknown', 'conflicting', 'truncated'}


def failure_marker(raw, exit_code):
    if len(raw) > 4096: return 'truncated'
    try: lines = raw.decode('utf-8-sig').splitlines()
    except UnicodeError: return 'unknown'
    if not lines or all(not line for line in lines): return 'missing'
    marker = 'PROFILE_OBSERVER_POLL_FIXTURE_FAILED'
    if lines.count(marker) > 1 or marker in lines and MARKERS[1] in lines: return 'conflicting'
    return 'known' if lines == [marker] and exit_code == 1 else 'unknown'


def chain_safe(value):
    need(isinstance(value, dict) and set(value) == {'schema', 'kind', 'parseFilesExpected',
        'parseFilesVerified', 'casesExpected', 'casesCompleted', 'passed', 'stages', 'failure'})
    need(type(value['schema']) is int and value['schema'] == 1 and value['kind'] == 'pure-poll-fixture')
    need(type(value['parseFilesExpected']) is int and value['parseFilesExpected'] == 4)
    need(type(value['casesExpected']) is int and value['casesExpected'] == 6)
    need(type(value['passed']) is bool and isinstance(value['stages'], dict) and set(value['stages']) == set(STAGES))
    for stage in value['stages'].values():
        need(isinstance(stage, dict) and set(stage) == {'runtimeExecuted', 'exitCode', 'passed', 'code'})
        need(type(stage['runtimeExecuted']) is bool and type(stage['passed']) is bool)
        need(stage['exitCode'] is None or type(stage['exitCode']) is int and -2147483648 <= stage['exitCode'] <= 4294967295)
        need(stage['code'] in {'not-run','pass','fixture-rejected','command-timeout','command-unavailable'})
        need(stage['passed'] is (stage['code'] == 'pass'))
        need(stage['code'] not in {'not-run','command-unavailable'} or not stage['runtimeExecuted'] and stage['exitCode'] is None)
        need(stage['code'] != 'command-timeout' or stage['runtimeExecuted'] and stage['exitCode'] is None)
        need(stage['code'] not in {'pass','fixture-rejected'} or stage['runtimeExecuted'] and type(stage['exitCode']) is int)
        need(not stage['passed'] or stage['exitCode'] == 0)
    parse, poll = (value['stages'][key] for key in STAGES)
    need(parse['passed'] or poll['code'] == 'not-run')
    need(type(value['parseFilesVerified']) is int and value['parseFilesVerified'] == (4 if parse['passed'] else 0))
    need(type(value['casesCompleted']) is int and value['casesCompleted'] == (6 if poll['passed'] else 0))
    need(value['passed'] is (parse['passed'] and poll['passed']))
    need(isinstance(value['failure'], str) and value['failure'] in FAILURES)
    need(value['failure'] == 'not-applicable' if poll['passed'] else value['failure'] == 'not-observed' if poll['code'] != 'fixture-rejected' else value['failure'] not in {'not-applicable','not-observed'})
    need(value['failure'] != 'known' or poll['exitCode'] == 1)
    return value


def run_chain(module, source, parser, root):
    result = {'schema': 1, 'kind': 'pure-poll-fixture', 'parseFilesExpected': 4,
        'parseFilesVerified': 0, 'casesExpected': 6, 'casesCompleted': 0, 'passed': False,
        'stages': {key: {'runtimeExecuted': False, 'exitCode': None, 'passed': False, 'code': 'not-run'} for key in STAGES},
        'failure': 'not-observed'}
    scripts = (parser, source / FILES['pollFixture'])
    for key, script, marker in zip(STAGES, scripts, MARKERS):
        private = root / key; private.mkdir(exist_ok=False)
        result['stages'][key] = module.run_stage(script, marker, private)
        if key == 'parse' and result['stages'][key]['passed']: result['parseFilesVerified'] = 4
        if key == 'poll':
            stage = result['stages'][key]
            if stage['passed']:
                result.update(casesCompleted=6, failure='not-applicable')
            elif stage['code'] == 'fixture-rejected':
                try:
                    with (private / 'stdout.log').open('rb') as stream: result['failure'] = failure_marker(stream.read(4097), stage['exitCode'])
                except Exception: result['failure'] = 'missing'
        if not result['stages'][key]['passed']: break
    result['passed'] = all(stage['passed'] for stage in result['stages'].values())
    return chain_safe(result)


def publish(identity, preflight, outcome, validator):
    need(outcome in OUTCOMES)
    files = {'identity.json': identity_safe(identity)}
    if preflight is not None:
        need(validator is not None)
        files['poll-preflight.json'] = validator(preflight)
        need(preflight['schema'] == 1)
    if outcome in {'fixture-passed', 'fixture-failed'}:
        need(preflight is not None and identity['sourceVerified'] and identity['sourceStableAfter'] is True)
        need(identity['controlCommit'] is not None and identity['controlTree'] is not None)
        need(identity['pythonVersion'] is not None and identity['powershellVersion'] is not None and identity['controlParserSha256'] is not None)
        need(preflight['passed'] is (outcome == 'fixture-passed'))
    canonical = lambda value: json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
    record = {'schema': 1, 'fixtureOnly': True, 'observationCap': 1, 'pr200Blocked': True,
              'outcome': outcome, 'files': files, 'fileSha256': {key: sha(canonical(value)) for key, value in files.items()}}
    raw = canonical(record)
    need(len(raw) <= 30000)
    body = 'BEGIN_PR220_POLL_FIXTURE_RECORD sha256:' + sha(raw) + '\n' + json.dumps(record, sort_keys=True, indent=2) + '\nEND_PR220_POLL_FIXTURE_RECORD\n'
    need(len(body.encode()) <= 60000 and max(map(len, body.splitlines())) <= 2000)
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a', encoding='utf-8') as summary:
        summary.write('```json\n' + body + '```\n')
    print(body, end='', flush=True)


def observe(source, control, root):
    identity = {'schema': 1, 'controlCommit': None, 'controlTree': None,
        'fixtureSourceCommit': SOURCE, 'fixtureSourceTree': TREE, 'reviewedPatchSha256': PATCH,
        'sourceVerified': False, 'sourceStableAfter': None, 'sourceFileSha256': {}, 'controlParserSha256': None,
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
        need(source == control.parent / 'fixture-source')
        parser = control / 'diagnostics/pr200-fresh/parse-poll-sources.ps1'
        parser_bytes = parser.read_bytes()
        original = git(control, 'show', commit + ':diagnostics/pr200-fresh/parse-poll-sources.ps1')
        need(parser_bytes in (original, original.replace(b'\n', b'\r\n')))
        identity['controlParserSha256'] = sha(parser_bytes)
        module = load_preflight(source)
        root.mkdir(exist_ok=False)
        outcome = 'recorder-rejected'
        data = run_chain(module, source, parser, root)
        code = 0 if data['passed'] else 1
        identity['sourceStableAfter'] = (source_hashes(source) == identity['sourceFileSha256'] and parser.read_bytes() == parser_bytes)
        need(identity['sourceStableAfter'])
        outcome = 'fixture-passed' if code == 0 else 'fixture-failed'
    except Exception:
        pass  # No exception type/text, raw output or unvalidated value is copied.
    publish(identity, data, outcome, chain_safe if data is not None else None)
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
