"""Select conservative native CI work and verify its terminal step evidence."""
import argparse
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
LANES = {'mac', 'windows', 'mac-development', 'mac-status', 'windows-owner'}
EXCLUSIVE = {
    'ui/mac-development/': {'mac-development'},
    'ui/mac-status/': {'mac-status'},
    'tools/windows-owner-probe/': {'windows-owner'},
    'tools/native-driver-trace/': {'windows'},
}


def require(condition):
    if not condition:
        raise ValueError('invalid scope evidence')


def sha(value):
    require(isinstance(value, str) and re.fullmatch(r'[0-9a-f]{40}', value) is not None)
    require(value != '0' * 40)
    return value


def git(repo, *args):
    return subprocess.run(['git', '-C', str(repo), *args], check=True,
                          capture_output=True, timeout=30).stdout


def changed_paths(repo, event, name, event_sha):
    sha(event_sha)
    if name == 'pull_request':
        head = sha(event['pull_request']['head']['sha'])
        base = sha(event['pull_request']['base']['sha'])
        # Compare the whole PR, not only its newest commit or GitHub's limited path list.
        bases = git(repo, 'merge-base', '--all', base, head).decode('ascii').splitlines()
        require(len(bases) == 1)
        comparison = sha(bases[0])
    else:
        require(name == 'push')
        head, base = sha(event['after']), sha(event['before'])
        require(head == event_sha)
        git(repo, 'merge-base', '--is-ancestor', base, head)
        comparison = base
    require(git(repo, 'rev-parse', 'HEAD').decode('ascii').strip() == head)
    raw = git(repo, 'diff', '--no-ext-diff', '--no-renames', '--name-only', '-z', comparison, head, '--')
    require(not raw or raw.endswith(b'\0'))
    # Both sides of renamed files count. Undecodable names become unknown/shared paths.
    paths = [path.decode('utf-8', errors='surrogateescape') for path in raw.split(b'\0')[:-1]]
    return paths, base, comparison, head


def selected_lanes(paths):
    if not paths:
        return LANES.copy(), 'empty_diff_run_all'
    selected = set()
    for path in paths:
        parts = PurePosixPath(path).parts
        # CI, shared contracts/helpers, manifests/locks and unrecognized paths run all.
        if (not parts or any(p in {'.', '..'} for p in parts) or '\\' in path
                or any(ord(c) < 32 or ord(c) >= 127 for c in path)
                or path.endswith('.lock') or parts[-1] in {'go.mod', 'go.sum', 'Cargo.toml', 'package.json', 'package-lock.json', 'Package.resolved'}):
            return LANES.copy(), 'shared_or_unknown_run_all'
        if path == 'README.md' or (path.startswith('docs/') and path.endswith('.md')):
            continue
        matches = [lanes for prefix, lanes in EXCLUSIVE.items() if path.startswith(prefix)]
        if not matches:
            return LANES.copy(), 'shared_or_unknown_run_all'
        selected.update(matches[0])
    return selected, 'reviewed_exclusive_paths'


def append_env_file(name, value):
    if filename := os.environ.get(name):
        with Path(filename).open('a', encoding='utf-8') as stream:
            stream.write(value)


def select(args):
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text(encoding='utf-8'))
    paths, base, comparison, head = changed_paths(args.repo, event, os.environ['GITHUB_EVENT_NAME'], os.environ['GITHUB_SHA'])
    lanes, reason = selected_lanes(paths)
    applicable = args.lane in lanes
    report = {'schema_version': 1, 'lane': args.lane, 'base_sha': base,
              'comparison_sha': comparison, 'head_sha': head, 'changed_files': len(paths),
              'applicability': 'applicable' if applicable else 'not_applicable', 'reason': reason}
    append_env_file('GITHUB_OUTPUT', f"selected={str(applicable).lower()}\n")
    append_env_file('GITHUB_STEP_SUMMARY', '\nCI scope selection (not a test result):\n\n```json\n' + json.dumps(report, sort_keys=True) + '\n```\n')
    return report


def identifiers(value):
    result = value.split(',')
    require(bool(result) and len(set(result)) == len(result))
    require(all(re.fullmatch(r'[A-Za-z_][A-Za-z0-9_-]*', part) for part in result))
    return set(result)


def gate(args):
    steps = json.loads(os.environ['CI_SCOPE_STEPS'])
    require(isinstance(steps, dict))
    require(steps['ci-scope']['outcome'] == 'success')
    selected = steps['ci-scope']['outputs']['selected']
    require(selected in {'true', 'false'})
    required = identifiers(args.required)
    optional = identifiers(args.optional) if args.optional else set()
    require(not required.intersection(optional))
    work = required | optional
    require(set(steps) == work | {'ci-checkout', 'ci-scope'})
    require(steps['ci-checkout']['outcome'] == 'success')
    for name in work:
        outcome = steps[name]['outcome']
        if selected == 'false':
            require(outcome == 'skipped')
        else:
            require(outcome == 'success' or (name in optional and outcome == 'skipped'))
    state = 'passed' if selected == 'true' else 'not_applicable'
    report = {'schema_version': 1, 'state': state, 'work_steps': len(work)}
    summary = ('Applicable work completed successfully.' if state == 'passed'
               else 'Not applicable to this change. Native/build tests were not run; this is not test success.')
    append_env_file('GITHUB_STEP_SUMMARY', '\nCI scope gate: ' + summary + '\n')
    return report


class SafeParser(argparse.ArgumentParser):
    def error(self, message):
        self.exit(2, 'CI_SCOPE_INVALID\n')


def main():
    parser = SafeParser(description=__doc__)
    sub = parser.add_subparsers(dest='mode', required=True)
    selection = sub.add_parser('select')
    selection.add_argument('--lane', choices=sorted(LANES), required=True)
    selection.add_argument('--repo', type=Path, default=ROOT)
    assertion = sub.add_parser('gate')
    assertion.add_argument('--required', required=True)
    assertion.add_argument('--optional', default='')
    args = parser.parse_args()
    try:
        report = select(args) if args.mode == 'select' else gate(args)
    except (OSError, ValueError, KeyError, TypeError, UnicodeError, subprocess.SubprocessError):
        print('CI_SCOPE_INVALID', file=sys.stderr)
        return 1
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
