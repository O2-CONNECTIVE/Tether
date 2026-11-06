import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts/check_ci_checkout.py'
SPEC = importlib.util.spec_from_file_location('checkout', SCRIPT)
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)


class ExactCheckoutTests(unittest.TestCase):
    def invoke(self, name, event, event_sha):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            payload, summary = root / 'event.json', root / 'summary.md'
            payload.write_text(json.dumps(event))
            env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR')}
            env.update(GITHUB_EVENT_NAME=name, GITHUB_EVENT_PATH=str(payload),
                       GITHUB_SHA=event_sha, GITHUB_STEP_SUMMARY=str(summary))
            result = subprocess.run([sys.executable, '-B', str(SCRIPT)], env=env,
                                    text=True, capture_output=True, cwd=root)
            return result, summary.read_text() if summary.exists() else ''

    def test_actual_git_head_distinguished_from_synthetic_event(self):
        head, tree = subprocess.check_output(['git', '-C', str(ROOT), 'rev-parse', 'HEAD', 'HEAD^{tree}'], text=True).splitlines()
        result, summary = self.invoke('pull_request', {'pull_request': {'head': {'sha': head}, 'base': {'sha': head}}}, 'a' * 40)
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        self.assertEqual((report['checkoutSha'], report['checkoutTree']), (head, tree))
        self.assertEqual(report['eventSha'], 'a' * 40)
        self.assertTrue(report['syntheticEventDiffers'])
        self.assertIn(head, summary)
        self.assertIn(tree, summary)

    def test_wrong_head_is_failure_without_success_summary(self):
        result, summary = self.invoke('pull_request', {'pull_request': {'head': {'sha': '0' * 40}, 'base': {'sha': 'a' * 40}}}, 'a' * 40)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, '')
        self.assertEqual(summary, '')
        self.assertIn('CI_EXACT_CHECKOUT_FAILED', result.stderr)

    def test_exact_push_and_mismatched_push(self):
        head = subprocess.check_output(['git', '-C', str(ROOT), 'rev-parse', 'HEAD'], text=True).strip()
        result, _ = self.invoke('push', {'after': head}, head)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(json.loads(result.stdout)['syntheticEventDiffers'])
        result, _ = self.invoke('push', {'after': '0' * 40}, head)
        self.assertNotEqual(result.returncode, 0)

    def test_malformed_or_unsupported_event_is_not_success(self):
        for name, event, event_sha in [('pull_request', {}, 'a' * 40),
                                      ('push', {'after': 'a' * 40}, 'invalid'),
                                      ('workflow_dispatch', {}, 'a' * 40)]:
            with self.subTest(name=name):
                result, summary = self.invoke(name, event, event_sha)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(summary, '')
