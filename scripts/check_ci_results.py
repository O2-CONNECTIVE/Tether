"""Read-only, fail-closed aggregation of a complete, fixed-source CI snapshot."""
import argparse
import json
from pathlib import Path
import re
import sys

from check_ci_checkout import sha

CONCLUSIONS = {'success', 'failure', 'skipped', 'cancelled', 'timed_out',
               'neutral', 'action_required', 'stale', 'startup_failure'}
STATUSES = {'queued', 'in_progress', 'waiting', 'pending', 'requested', 'completed'}


def require(condition):
    if not condition:
        raise ValueError('invalid evidence')


def positive(value):
    require(type(value) is int and value > 0)
    return value


def label(value):
    require(isinstance(value, str) and 0 < len(value) <= 512)
    require(not any(ord(char) < 32 or ord(char) == 127 for char in value))
    return value


def state(record):
    require(record['status'] in STATUSES)
    if record['status'] != 'completed':
        require(record['conclusion'] is None)
        return 'pending'
    require(record['conclusion'] in CONCLUSIONS)
    return record['conclusion']


def counted(container, key):
    values = container[key]
    require(isinstance(values, list))
    require(type(container['total_count']) is int and container['total_count'] == len(values))
    return values


def validate_policy(policy):
    require(type(policy['schema_version']) is int and policy['schema_version'] == 1)
    required = policy['workflows']
    require(isinstance(required, dict) and bool(required))
    for path, names in required.items():
        require(re.fullmatch(r'\.github/workflows/[A-Za-z0-9_-]+\.ya?ml', path) is not None)
        require(isinstance(names, list) and bool(names))
        for name in names:
            label(name)
        require(len(names) == len(set(names)))
    return required


def validate_runs(snapshot, repository):
    require(type(snapshot['schema_version']) is int and snapshot['schema_version'] == 1)
    require(snapshot['repository'] == repository)
    runs = counted(snapshot, 'workflow_runs')
    identities = set()
    run_metadata = {}
    numbers = {}
    for run in runs:
        identity = positive(run['id']), positive(run['run_attempt'])
        require(identity not in identities)
        identities.add(identity)
        label(run['path'])
        label(run['event'])
        sha(run['head_sha'])
        positive(run['run_number'])
        number_key = (run['path'], run['run_number'])
        require(numbers.get(number_key, run['id']) == run['id'])
        numbers[number_key] = run['id']
        state(run)
        metadata = (run['path'], run['event'], run['head_sha'], run['run_number'])
        require(run_metadata.get(run['id'], metadata) == metadata)
        run_metadata[run['id']] = metadata
        names, ids = set(), set()
        for job in counted(run['jobs'], 'jobs'):
            positive(job['id'])
            name = label(job['name'])
            require(name not in names and job['id'] not in ids)
            names.add(name)
            ids.add(job['id'])
            require(positive(job['run_id']) == run['id'])
            require(positive(job['run_attempt']) == run['run_attempt'])
            require(sha(job['head_sha']) == run['head_sha'])
            state(job)
    return runs


def aggregate(snapshot, policy, head, repository, event):
    sha(head)
    require(re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository) is not None)
    require(event in {'push', 'pull_request'})
    required = validate_policy(policy)
    runs = validate_runs(snapshot, repository)
    results = []
    for path, names in required.items():
        matching = [run for run in runs if run['path'] == path and run['event'] == event]
        current = [run for run in matching if run['head_sha'] == head]
        result = {'path': path, 'state': 'stale' if matching else 'missing', 'jobs': []}
        if current:
            # A later rerun must supersede earlier green, even while queued.
            selected = max(current, key=lambda run: (run['run_number'], run['id'], run['run_attempt']))
            result.update(run_id=selected['id'], run_attempt=selected['run_attempt'], state=state(selected))
            by_name = {job['name']: job for job in selected['jobs']['jobs']}
            result['jobs'] = [{'name': name, 'state': state(by_name[name]) if name in by_name else 'missing'}
                              for name in names]
            if result['state'] == 'success' and any(job['state'] != 'success' for job in result['jobs']):
                result['state'] = 'jobs_not_successful'
        results.append(result)
    return {'schema_version': 1, 'repository': repository, 'head_sha': head, 'event': event,
            'success': all(result['state'] == 'success' for result in results), 'workflows': results}


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result)
        result[key] = value
    return result


def read_json(path):
    # Reject duplicate keys rather than silently allowing later fields to override evidence.
    return json.loads(Path(path).read_text(encoding='utf-8'), object_pairs_hook=unique_object)


class SafeParser(argparse.ArgumentParser):
    def error(self, message):
        self.exit(2, "CI_RESULTS_INVALID\n")


def main():
    parser = SafeParser(description=__doc__)
    parser.add_argument('--snapshot', required=True)
    parser.add_argument('--policy', required=True)
    parser.add_argument('--sha', required=True)
    parser.add_argument('--repository', required=True)
    parser.add_argument('--event', required=True, choices=['push', 'pull_request'])
    args = parser.parse_args()
    try:
        report = aggregate(read_json(args.snapshot), read_json(args.policy), args.sha,
                           args.repository, args.event)
    except (OSError, ValueError, KeyError, TypeError, RecursionError):
        print('CI_RESULTS_INVALID', file=sys.stderr)
        return 2
    print(json.dumps(report, sort_keys=True, ensure_ascii=True))
    return 0 if report['success'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
