"""Public CLI fixtures for conservative CI selection and terminal step evidence."""
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts/ci_scope.py'
LANES = ['mac', 'windows-shared', 'windows-native', 'mac-development', 'mac-status', 'windows-owner']


class ScopeCLITests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.git('init', '-q')
        self.git('config', 'user.email', 'fixture@example.invalid')
        self.git('config', 'user.name', 'Fixture')
        self.write('README.md', 'base\n')
        self.commit()
        self.base = self.git('rev-parse', 'HEAD')

    def git(self, *args):
        return subprocess.check_output(['git', '-C', str(self.root), *args], text=True, stderr=subprocess.DEVNULL).strip()

    def write(self, path, content='changed\n'):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content, encoding='utf-8')

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-qm', 'fixture')
        return self.git('rev-parse', 'HEAD')

    def invoke(self, lane='mac', event=None, name='push', event_sha=None):
        head = self.git('rev-parse', 'HEAD')
        payload = {'before': self.base, 'after': head} if event is None else event
        event_file = self.root / '.git/event.json'
        output, summary = self.root / '.git/output', self.root / '.git/summary'
        event_file.write_text(json.dumps(payload))
        output.write_text(''); summary.write_text('')
        env = dict(os.environ, GITHUB_EVENT_NAME=name, GITHUB_EVENT_PATH=str(event_file),
                   GITHUB_SHA=event_sha or head, GITHUB_OUTPUT=str(output), GITHUB_STEP_SUMMARY=str(summary))
        result = subprocess.run([sys.executable, '-I', str(SCRIPT), 'select', '--lane', lane,
                                 '--repo', str(self.root)], env=env, capture_output=True, text=True)
        return result, json.loads(result.stdout) if result.stdout else None, output.read_text(), summary.read_text()

    def assert_lanes(self, expected):
        for lane in LANES:
            with self.subTest(lane=lane):
                result, report, output, summary = self.invoke(lane)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(report['applicability'], 'applicable' if lane in expected else 'not_applicable')
                self.assertEqual(output, f"selected={str(lane in expected).lower()}\n")
                self.assertIn('not a test result', summary)

    def test_documentation_only_reports_not_applicable_without_claiming_tests(self):
        self.write('README.md'); self.write('docs/architecture.md'); self.commit()
        self.assert_lanes([])
        self.assertEqual(self.invoke()[1]['changed_files'], 2)

    def test_independent_mac_viewers_select_only_their_own_work(self):
        self.write('ui/mac-development/Sources/Screen.swift'); self.commit()
        self.assert_lanes(['mac-development'])
        self.base = self.git('rev-parse', 'HEAD')
        self.write('ui/mac-status/Sources/Screen.swift'); self.commit()
        self.assert_lanes(['mac-status'])

    def test_isolated_windows_tools_and_mixed_changes_keep_union(self):
        self.write('tools/native-driver-trace/driver.patch'); self.commit()
        self.assert_lanes(['windows-shared', 'windows-native'])
        self.write('tools/windows-owner-probe/Probe.cs'); self.write('ui/mac-status/Sources/Screen.swift'); self.commit()
        self.assert_lanes(['windows-shared', 'windows-native', 'windows-owner', 'mac-status'])

    def test_backend_shared_ui_helpers_workflows_contracts_and_locks_run_all(self):
        paths = ['backend/internal/agentgateway/gateway.go', 'backend/internal/ssh/server.go',
                 'ui/tauri/src-tauri/src/mac.rs', 'scripts/check_mac_library_ci.py',
                 '.github/workflows/mac-native-source.yml', 'contracts/session.json',
                 'ui/mac-development/Package.resolved', 'ui/mac-status/Cargo.lock', 'unknown.txt']
        for path in paths:
            with self.subTest(path=path):
                self.base = self.git('rev-parse', 'HEAD')
                self.write(path); self.commit()
                self.assert_lanes(LANES)

    def test_audited_backend_changes_keep_shared_checks_without_windows_gui(self):
        initial_base = self.base
        paths = [
            'backend/cmd/rescue-gateway/access_refresh.go',
            'backend/cmd/rescue-gateway/access_refresh_test.go',
            'backend/cmd/rescue-gateway/discovery_pilot.go',
            'backend/cmd/rescue-gateway/main.go',
            'backend/cmd/tether-echo-probe/probe.go',
            'backend/cmd/tether-echo-probe/probe_test.go',
            'backend/internal/agentgateway/access_rotation.go',
            'backend/internal/agentgateway/access_rotation_config_test.go',
            'backend/internal/agentgateway/config.go',
            'scripts/go_ci.py', 'scripts/tests/test_go_ci.py',
        ]
        for path in paths:
            with self.subTest(path=path):
                self.base = self.git('rev-parse', 'HEAD')
                self.write(path); self.commit()
                self.assert_lanes(set(LANES) - {'windows-native'})
        self.base = initial_base
        for path in ['docs/access-key-rotation-candidate.md', 'docs/agent-cli.md',
                     'docs/agent-gateway.md', 'docs/go-ci.md']:
            self.write(path)
        self.commit()
        self.assert_lanes(set(LANES) - {'windows-native'})
        self.assertEqual(self.invoke()[1]['changed_files'], 15)

    def test_backend_mixed_with_native_inputs_and_unknown_paths_runs_all(self):
        paths = ['ui/tauri/src/main.tsx', 'ui/tauri/src-tauri/build.rs',
                 'ui/tauri/native-smoke/setup.ps1', 'contracts/session.json',
                 'backend/go.mod', 'backend/go.sum', 'backend/new.go',
                 'backend/internal/agentgateway/unknown.go',
                 'scripts/go_ci_extra.py', 'scripts/tests/test_go_ci_extra.py',
                 'scripts/package_tauri_evaluation.py', 'scripts/ci_scope.py',
                 'docs/tauri-evaluation-readme.txt', 'LICENSE',
                 'ui/tauri/package-lock.json', 'ui/tauri/src-tauri/Cargo.lock',
                 '.github/workflows/tauri-evaluation.yml']
        for path in paths:
            with self.subTest(path=path):
                self.base = self.git('rev-parse', 'HEAD')
                self.write('backend/cmd/rescue-gateway/access_refresh.go', path)
                self.write(path); self.commit()
                self.assert_lanes(LANES)

    def test_audited_backend_deletion_is_exempt_but_rename_from_shared_is_not(self):
        path = 'backend/cmd/rescue-gateway/access_refresh.go'
        self.write(path); self.commit()
        self.base = self.git('rev-parse', 'HEAD')
        self.git('rm', path); self.commit()
        self.assert_lanes(set(LANES) - {'windows-native'})
        self.write('ui/tauri/input.rs'); self.commit()
        self.base = self.git('rev-parse', 'HEAD')
        (self.root / path).parent.mkdir(parents=True, exist_ok=True)
        self.git('mv', 'ui/tauri/input.rs', path); self.commit()
        self.assert_lanes(LANES)
        self.base = self.git('rev-parse', 'HEAD')
        self.git('mv', path, 'ui/tauri/input.rs'); self.commit()
        self.assert_lanes(LANES)

    def test_rename_from_shared_to_exclusive_keeps_both_sides(self):
        self.write('shared.txt'); self.commit()
        self.base = self.git('rev-parse', 'HEAD')
        self.git('mv', 'shared.txt', 'README.md.old')
        (self.root / 'ui/mac-status').mkdir(parents=True)
        self.git('mv', 'README.md.old', 'ui/mac-status/copied.swift'); self.commit()
        self.assert_lanes(LANES)
        self.assertEqual(self.invoke()[1]['changed_files'], 2)

    def test_deletion_and_over_300_paths_are_not_truncated(self):
        self.write('backend/shared.go'); self.commit()
        self.base = self.git('rev-parse', 'HEAD')
        self.git('rm', 'backend/shared.go')
        for index in range(305):
            self.write(f'docs/guide-{index:03}.md')
        self.commit()
        self.assert_lanes(LANES)
        self.assertEqual(self.invoke()[1]['changed_files'], 306)

    def test_nul_delimited_unusual_path_is_unknown_and_not_echoed(self):
        self.write('docs/a SYNTHETIC_SECRETé.md'); self.commit()
        self.assert_lanes(LANES)
        result, _, _, summary = self.invoke()
        self.assertNotIn('SYNTHETIC_SECRET', result.stdout + result.stderr + summary)

    def test_empty_diff_runs_all(self):
        self.assert_lanes(LANES)
        self.assertEqual(self.invoke()[1]['reason'], 'empty_diff_run_all')

    def test_pr_uses_merge_base_and_all_branch_commits_not_synthetic_event(self):
        self.git('checkout', '-qb', 'candidate')
        self.write('ui/mac-status/Sources/Screen.swift'); self.commit()
        self.write('docs/guide.md'); head = self.commit()
        self.git('checkout', '-qb', 'advanced-base', self.base)
        self.write('backend/unrelated.go'); base = self.commit()
        self.git('checkout', 'candidate')
        event = {'pull_request': {'head': {'sha': head}, 'base': {'sha': base}}}
        result, report, _, _ = self.invoke('mac-status', event, 'pull_request', 'a' * 40)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(report['applicability'], 'applicable')
        self.assertEqual(report['base_sha'], base)
        self.assertEqual(report['comparison_sha'], self.base)
        self.assertEqual(report['head_sha'], head)
        self.assertEqual(report['changed_files'], 2)
        for lane in ['windows-shared', 'windows-native']:
            self.assertEqual(self.invoke(lane, event, 'pull_request', 'a' * 40)[1]['applicability'], 'not_applicable')

    def test_missing_or_invalid_history_wrong_head_event_and_nonlinear_push_fail_closed(self):
        head = self.git('rev-parse', 'HEAD')
        cases = [({'before': 'b' * 40, 'after': head}, 'push', head),
                 ({'before': '0' * 40, 'after': head}, 'push', head),
                 ({'before': self.base, 'after': head}, 'push', 'a' * 40),
                 ({'before': self.base, 'after': head}, 'unknown', head),
                 ({'pull_request': {'head': {'sha': 'a' * 40}, 'base': {'sha': self.base}}}, 'pull_request', head)]
        for event, name, event_sha in cases:
            result, report, output, _ = self.invoke(event=event, name=name, event_sha=event_sha)
            self.assertNotEqual(result.returncode, 0)
            self.assertIsNone(report); self.assertEqual(output, '')
            self.assertEqual(result.stderr.strip(), 'CI_SCOPE_INVALID')
        self.write('new.txt'); newer = self.commit()
        self.git('checkout', '--detach', head)
        self.assertNotEqual(self.invoke(event={'before': newer, 'after': head})[0].returncode, 0)


class ScopeGateTests(unittest.TestCase):
    def invoke(self, selected='true', changes=None):
        steps = {'ci-checkout': {'outcome': 'success'},
                 'ci-scope': {'outcome': 'success', 'outputs': {'selected': selected}},
                 'test': {'outcome': 'success' if selected == 'true' else 'skipped'},
                 'cache': {'outcome': 'skipped'}}
        if changes:
            changes(steps)
        env = dict(os.environ, CI_SCOPE_STEPS=json.dumps(steps))
        env.pop('GITHUB_STEP_SUMMARY', None)
        result = subprocess.run([sys.executable, '-I', str(SCRIPT), 'gate', '--required', 'test',
                                 '--optional', 'cache'], env=env, capture_output=True, text=True)
        return result, json.loads(result.stdout) if result.stdout else None

    def test_applicable_and_not_applicable_have_distinct_success_states(self):
        for value, state in [('true', 'passed'), ('false', 'not_applicable')]:
            result, report = self.invoke(value)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(report['state'], state)

    def test_every_unexpected_required_outcome_fails_even_when_optional_cache_is_skipped(self):
        for outcome in ['skipped', 'cancelled', 'failure', 'unknown']:
            self.assertNotEqual(self.invoke(changes=lambda s: s['test'].update(outcome=outcome))[0].returncode, 0)

    def test_optional_failure_missing_extra_and_detector_failure_never_pass(self):
        changes = [lambda s: s['cache'].update(outcome='failure'), lambda s: s.pop('test'),
                   lambda s: s.update(extra={'outcome': 'success'}),
                   lambda s: s['ci-scope'].update(outcome='failure'),
                   lambda s: s['ci-checkout'].update(outcome='failure')]
        for change in changes:
            self.assertNotEqual(self.invoke(changes=change)[0].returncode, 0)
        for value in ['', 'True', 'yes']:
            self.assertNotEqual(self.invoke(value)[0].returncode, 0)

    def test_not_applicable_rejects_executed_failed_or_missing_work(self):
        for outcome in ['success', 'failure', 'cancelled']:
            self.assertNotEqual(self.invoke('false', lambda s: s['test'].update(outcome=outcome))[0].returncode, 0)
        self.assertNotEqual(self.invoke('false', lambda s: s.pop('test'))[0].returncode, 0)


class ScopeWorkflowTests(unittest.TestCase):
    def test_windows_gui_and_shared_checks_have_distinct_lanes(self):
        for name in ['tauri-native-smoke', 'tauri-evaluation', 'tauri-startup-diagnostic']:
            source = (ROOT / '.github/workflows' / (name + '.yml')).read_text()
            self.assertIn('select --lane windows-native', source)
            self.assertNotIn('continue-on-error:', source)
        source = (ROOT / '.github/workflows/tauri-shell.yml').read_text()
        self.assertIn('select --lane windows-shared', source)
        for command in ['npm run typecheck', 'npm run lint', 'npm run knip',
                        'npm test', 'npm run test:browser', 'cargo +1.99.1 test',
                        'cargo +1.99.1 clippy']:
            self.assertIn(command, source)
        self.assertNotIn('continue-on-error:', source)
        for path in (ROOT / '.github/workflows').glob('*.yml'):
            self.assertNotRegex(path.read_text(), r'--lane windows(?:\s|$)')

    def test_existing_jobs_are_always_created_and_all_work_is_in_the_gate(self):
        workflows = ['mac-development', 'mac-native-source', 'mac-resident-gui',
                     'mcp-loopback', 'producer-connection', 'tauri-evaluation',
                     'tauri-native-smoke', 'tauri-shell', 'tauri-startup-diagnostic',
                     'tether-mac-status', 'windows-owner-probe']
        jobs = 0
        for name in workflows:
            source = (ROOT / '.github/workflows' / (name + '.yml')).read_text()
            self.assertNotRegex(source, r'(?m)^    (paths|paths-ignore|if):')
            for job in re.split(r'(?m)^  [a-z][a-z-]+:\n', source.split('jobs:\n', 1)[1])[1:]:
                jobs += 1
                steps = re.split(r'(?m)(?=^      - )', job.split('    steps:\n', 1)[1])
                steps = [step for step in steps if step.strip()]
                self.assertIn('id: ci-checkout', steps[0])
                self.assertIn('fetch-depth: 0', steps[0])
                self.assertIn('id: ci-scope', steps[1])
                self.assertNotRegex(steps[1], r'(?m)^        if:')
                self.assertIn('if: always()', steps[-1])
                self.assertIn('CI_SCOPE_STEPS: ${{ toJSON(steps) }}', steps[-1])
                command = steps[-1].split('run: ', 1)[1].strip().split()
                required = set(command[command.index('--required') + 1].split(','))
                optional = set(command[command.index('--optional') + 1].split(',')) if '--optional' in command else set()
                ids = []
                for step in steps[2:-1]:
                    identity = re.findall(r'(?m)^        id: (.+)$', step)
                    self.assertEqual(len(identity), 1)
                    ids.extend(identity)
                    self.assertIn("steps.ci-scope.outputs.selected == 'true'", step)
                    if "&& (always()) }}" in step:
                        self.assertIn(identity[0], required)
                self.assertEqual(len(ids), len(set(ids)))
                self.assertEqual(set(ids), required | optional)
                self.assertFalse(required & optional)
        self.assertEqual(jobs, 14)
        for name in ['python-offline', 'go-offline']:
            source = (ROOT / '.github/workflows' / (name + '.yml')).read_text()
            self.assertNotIn('ci_scope.py', source)
            self.assertNotRegex(source, r'(?m)^    (paths|paths-ignore|if):')


if __name__ == '__main__':
    unittest.main()
