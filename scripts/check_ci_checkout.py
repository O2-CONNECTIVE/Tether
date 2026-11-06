"""Require the event's exact source head; record merge-ref and tree separately."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]


def sha(value):
    if not isinstance(value, str) or re.fullmatch(r'[0-9a-f]{40}', value) is None:
        raise ValueError('invalid source revision')
    return value


def expected_head(event, name, event_sha):
    sha(event_sha)
    if name == 'pull_request':
        return sha(event['pull_request']['head']['sha'])
    if name == 'push':
        if sha(event['after']) != event_sha:
            raise ValueError('push revision mismatch')
        return event_sha
    raise ValueError('unsupported source event')


def run():
    if len(sys.argv) != 1:
        raise ValueError('unexpected checkout arguments')
    name = os.environ['GITHUB_EVENT_NAME']
    event_sha = os.environ['GITHUB_SHA']
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    expected = expected_head(event, name, event_sha)
    base = sha(event['pull_request']['base']['sha']) if name == 'pull_request' else None
    result = subprocess.run(['git', '-C', str(ROOT), 'rev-parse', 'HEAD', 'HEAD^{tree}'],
                            check=True, text=True, capture_output=True)
    actual, tree = map(sha, result.stdout.splitlines())
    if actual != expected:
        raise ValueError('checkout is not the exact event source head')
    evidence = {'checkoutSha': actual, 'checkoutTree': tree, 'expectedSourceHead': expected,
                'event': name, 'baseSha': base, 'eventSha': event_sha, 'syntheticEventDiffers': event_sha != actual}
    output = json.dumps(evidence, sort_keys=True)
    print(output)
    if summary := os.environ.get('GITHUB_STEP_SUMMARY'):
        with Path(summary).open('a') as stream:
            stream.write('\nChecked-out source (event SHA may be a synthetic merge ref):\n\n```json\n' + output + '\n```\n')


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
        print('CI_EXACT_CHECKOUT_FAILED', file=sys.stderr)
        raise SystemExit(1)
