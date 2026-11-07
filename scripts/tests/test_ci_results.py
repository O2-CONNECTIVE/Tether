"""Secret-free fixed-source CI aggregation fixtures; no GitHub calls."""
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts/check_ci_results.py'
SHA = 'a' * 40
REPO = 'example/project'
WORKFLOW = '.github/workflows/offline.yml'
POLICY = {'schema_version': 1, 'workflows': {WORKFLOW: ['Linux', 'Windows']}}


def snapshot():
    jobs = [dict(id=i, run_id=10, run_attempt=1, head_sha=SHA, name=name,
                 status='completed', conclusion='success')
            for i, name in enumerate(['Linux', 'Windows'], 1)]
    run = dict(id=10, run_number=5, run_attempt=1, path=WORKFLOW,
               head_sha=SHA, event='pull_request', status='completed',
               conclusion='success', jobs={'total_count': 2, 'jobs': jobs})
    return {'schema_version': 1, 'repository': REPO,
            'total_count': 1, 'workflow_runs': [run]}


class CIResultTests(unittest.TestCase):
    def invoke(self, data=None, policy=None, sha=SHA):
        with tempfile.TemporaryDirectory() as directory:
            source, required = Path(directory) / 'snapshot.json', Path(directory) / 'policy.json'
            source.write_text(json.dumps(snapshot() if data is None else data), encoding='utf-8')
            required.write_text(json.dumps(POLICY if policy is None else policy), encoding='utf-8')
            before = source.read_bytes(), required.read_bytes()
            result = subprocess.run([sys.executable, '-B', str(SCRIPT), '--snapshot', str(source),
                                     '--policy', str(required), '--sha', sha, '--repository', REPO,
                                     '--event', 'pull_request'], capture_output=True, text=True)
            self.assertEqual(before, (source.read_bytes(), required.read_bytes()))
            return result, json.loads(result.stdout) if result.stdout else None

    def test_success_requires_every_required_job(self):
        result, report = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(report['success'])
        self.assertEqual(report['head_sha'], SHA)
        self.assertEqual([j['state'] for j in report['workflows'][0]['jobs']], ['success'] * 2)

    def test_each_non_success_job_is_distinct_and_fails(self):
        for conclusion in ['skipped', 'cancelled', 'failure', 'timed_out', 'neutral', 'action_required', 'stale']:
            with self.subTest(conclusion=conclusion):
                data = snapshot()
                data['workflow_runs'][0]['jobs']['jobs'][0]['conclusion'] = conclusion
                result, report = self.invoke(data)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(report['workflows'][0]['jobs'][0]['state'], conclusion)

    def test_missing_workflow_and_job_and_all_skip_fail(self):
        data = snapshot()
        data.update(total_count=0, workflow_runs=[])
        result, report = self.invoke(data)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(report['workflows'][0]['state'], 'missing')
        data = snapshot()
        run = data['workflow_runs'][0]
        run['jobs']['jobs'].pop()
        run['jobs']['total_count'] = 1
        result, report = self.invoke(data)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(report['workflows'][0]['jobs'][1]['state'], 'missing')
        data = snapshot()
        for job in data['workflow_runs'][0]['jobs']['jobs']:
            job['conclusion'] = 'skipped'
        self.assertEqual(self.invoke(data)[0].returncode, 1)

    def test_stale_source_success_never_satisfies_current_head(self):
        data = snapshot()
        data['workflow_runs'][0]['head_sha'] = 'b' * 40
        for job in data['workflow_runs'][0]['jobs']['jobs']:
            job['head_sha'] = 'b' * 40
        result, report = self.invoke(data)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(report['workflows'][0]['state'], 'stale')

    def test_pending_or_failed_workflow_cannot_hide_behind_green_jobs(self):
        for status, conclusion, state in [('in_progress', None, 'pending'), ('completed', 'failure', 'failure'),
                                           ('completed', 'cancelled', 'cancelled')]:
            data = snapshot()
            data['workflow_runs'][0].update(status=status, conclusion=conclusion)
            result, report = self.invoke(data)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(report['workflows'][0]['state'], state)

    def test_latest_rerun_and_new_run_override_older_success(self):
        for same_run in [True, False]:
            data = snapshot()
            newer = copy.deepcopy(data['workflow_runs'][0])
            newer.update(run_attempt=2 if same_run else 1, id=10 if same_run else 11,
                         run_number=5 if same_run else 6, status='queued', conclusion=None)
            for job in newer['jobs']['jobs']:
                job.update(run_id=newer['id'], run_attempt=newer['run_attempt'])
            data['workflow_runs'].append(newer)
            data['total_count'] = 2
            result, report = self.invoke(data)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(report['workflows'][0]['state'], 'pending')

    def test_wrong_event_not_reused_and_other_sha_does_not_replace_current(self):
        data = snapshot()
        data['workflow_runs'][0]['event'] = 'push'
        self.assertEqual(self.invoke(data)[1]['workflows'][0]['state'], 'missing')
        data = snapshot()
        newer = copy.deepcopy(data['workflow_runs'][0])
        newer.update(id=11, run_number=6, head_sha='b' * 40)
        for job in newer['jobs']['jobs']:
            job.update(run_id=11, head_sha='b' * 40)
        data['workflow_runs'].append(newer)
        data['total_count'] = 2
        self.assertEqual(self.invoke(data)[0].returncode, 0)

    def test_invalid_or_incomplete_evidence_fails_closed(self):
        variants = []
        data = snapshot(); data['total_count'] = 2; variants.append(data)
        data = snapshot(); data['repository'] = 'other/project'; variants.append(data)
        data = snapshot(); data['workflow_runs'][0]['jobs']['total_count'] = 3; variants.append(data)
        for field, value in [('run_id', 9), ('run_attempt', 2), ('head_sha', 'b' * 40),
                             ('conclusion', 'unknown'), ('status', 'unknown'), ('id', True)]:
            data = snapshot(); data['workflow_runs'][0]['jobs']['jobs'][0][field] = value; variants.append(data)
        data = snapshot(); data['workflow_runs'] *= 2; data['total_count'] = 2; variants.append(data)
        data = snapshot(); duplicate = copy.deepcopy(data['workflow_runs'][0]); duplicate['id'] = 11
        data['workflow_runs'].append(duplicate); data['total_count'] = 2; variants.append(data)
        data = snapshot(); data['workflow_runs'][0]['jobs']['jobs'][1]['name'] = 'Linux'; variants.append(data)
        for data in variants:
            with self.subTest(data=data):
                result, report = self.invoke(data)
                self.assertEqual(result.returncode, 2)
                self.assertIsNone(report)
                self.assertEqual(result.stderr.strip(), 'CI_RESULTS_INVALID')

    def test_pending_job_is_not_success(self):
        data = snapshot()
        data['workflow_runs'][0]['jobs']['jobs'][0].update(status='in_progress', conclusion=None)
        result, report = self.invoke(data)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(report['workflows'][0]['jobs'][0]['state'], 'pending')

    def test_zero_jobs_and_multiple_required_workflows(self):
        data = snapshot()
        data['workflow_runs'][0]['jobs'] = {'total_count': 0, 'jobs': []}
        self.assertEqual(self.invoke(data)[0].returncode, 1)
        policy = copy.deepcopy(POLICY)
        policy['workflows']['.github/workflows/other.yml'] = ['Other']
        result, report = self.invoke(policy=policy)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(report['workflows'][1]['state'], 'missing')

    def test_duplicate_json_keys_fail_without_echoing_input(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / 'duplicate.json'
            source.write_text('{"secret": 1, "secret": 2}', encoding='utf-8')
            result = subprocess.run([sys.executable, '-B', str(SCRIPT), '--snapshot', str(source),
                                     '--policy', str(source), '--sha', SHA, '--repository', REPO,
                                     '--event', 'push'], capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(result.stdout, '')
            self.assertEqual(result.stderr.strip(), 'CI_RESULTS_INVALID')

    def test_invalid_arguments_do_not_echo_input(self):
        for arguments in [['--event', 'SYNTHETIC_SECRET'], ['--SYNTHETIC_SECRET'], []]:
            result = subprocess.run([sys.executable, '-B', str(SCRIPT)] + arguments,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(result.stdout, '')
            self.assertEqual(result.stderr.strip(), 'CI_RESULTS_INVALID')
        result = subprocess.run([sys.executable, '-B', str(SCRIPT), '--help'],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        self.assertIn('--snapshot', result.stdout)

    def test_empty_policy_invalid_sha_and_inconsistent_status_fail_closed(self):
        for policy in [{'schema_version': 1, 'workflows': {}}, {'schema_version': 1, 'workflows': {WORKFLOW: []}}]:
            self.assertEqual(self.invoke(policy=policy)[0].returncode, 2)
        self.assertEqual(self.invoke(sha='main')[0].returncode, 2)
        data = snapshot(); data['workflow_runs'][0]['jobs']['jobs'][0]['status'] = 'queued'
        self.assertEqual(self.invoke(data)[0].returncode, 2)


if __name__ == '__main__':
    unittest.main()
