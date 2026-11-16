"""Exercise the issue triage label policy through the offline CLI.

The workflow auto-fixes only results that are unique, and fails on combinations
that need a human to choose which label to keep. These cases pin both sides.
"""

import json
from pathlib import Path
import subprocess
import sys
import unittest


CHECKER = Path(__file__).resolve().parents[1] / 'check_issue_triage.py'
CATEGORY = ['Area:Resident', 'Kind:Bug']


def evaluate(state, labels):
    args = [sys.executable, '-I', str(CHECKER), '--evaluate', '--state', state]
    for label in labels:
        args += ['--label', label]
    result = subprocess.run(args, capture_output=True, text=True, timeout=20)
    return result.returncode, json.loads(result.stdout)


class CategoryTests(unittest.TestCase):
    def test_classified_issue_needs_no_change(self):
        code, out = evaluate('open', CATEGORY + ['AFK'])
        self.assertEqual((code, out), (0, {'add': [], 'remove': [], 'violations': []}))

    def test_missing_kind_marks_needs_category(self):
        # 未分類は集計から漏れるので、目印を付けて見えるようにする。
        code, out = evaluate('open', ['Area:Resident'])
        self.assertEqual(code, 0)
        self.assertEqual(out['add'], ['Needs-Category'])

    def test_two_areas_are_not_a_classification(self):
        code, out = evaluate('open', ['Area:Resident', 'Area:Gateway-MCP', 'Kind:Bug'])
        self.assertEqual(out['add'], ['Needs-Category'])

    def test_unknown_area_is_not_a_classification(self):
        # O2-REPORT の Area を流用しても Tether の分類にはならない。
        code, out = evaluate('open', ['Area:Journal', 'Kind:Bug'])
        self.assertEqual(out['add'], ['Needs-Category'])

    def test_marker_is_removed_once_classified(self):
        code, out = evaluate('open', CATEGORY + ['Needs-Category'])
        self.assertEqual((out['add'], out['remove']), ([], ['Needs-Category']))

    def test_marker_is_kept_while_unclassified(self):
        code, out = evaluate('open', ['Needs-Category'])
        self.assertEqual((out['add'], out['remove']), ([], []))

    def test_excluded_issue_does_not_need_category(self):
        for label in ['Invalid', 'Duplicate', 'Wontfix']:
            with self.subTest(label=label):
                code, out = evaluate('closed', [label])
                self.assertEqual(out, {'add': [], 'remove': [], 'violations': []})

    def test_closed_issue_is_still_classified(self):
        code, out = evaluate('closed', [])
        self.assertEqual(out['add'], ['Needs-Category'])


class DestinationTests(unittest.TestCase):
    def violations(self, labels, state='open'):
        code, out = evaluate(state, CATEGORY + labels)
        return code, out['violations']

    def test_subtype_adds_parent_hitl(self):
        code, out = evaluate('open', CATEGORY + ['HITL:Decision'])
        self.assertEqual((code, out['add'], out['violations']), (0, ['HITL'], []))

    def test_afk_and_hitl_fail_without_auto_fix(self):
        # どちらを残すかは意味の判断なので、自動では外さない。
        code, violations = self.violations(['AFK', 'HITL', 'HITL:Decision'])
        self.assertEqual(code, 1)
        self.assertEqual(violations, ['destination.conflict'])

    def test_afk_with_subtype_only_is_still_a_conflict(self):
        # 人が AFK と HITL のどちらを残すか決めるまで、HITL を補って片側に寄せない。
        code, out = evaluate('open', CATEGORY + ['AFK', 'HITL:Verification'])
        self.assertEqual(code, 1)
        self.assertEqual(out['add'], [])
        self.assertIn('destination.conflict', out['violations'])

    def test_lifecycle_with_subtype_does_not_add_hitl(self):
        code, out = evaluate('open', CATEGORY + ['Needs-Info', 'HITL:Research'])
        self.assertEqual((code, out['add']), (1, []))

    def test_afk_cannot_be_blocked(self):
        code, violations = self.violations(['AFK', 'Blocked:Decision'])
        self.assertEqual((code, violations), (1, ['destination.blocked']))

    def test_hitl_requires_subtype(self):
        code, violations = self.violations(['HITL'])
        self.assertEqual((code, violations), (1, ['hitl.subtype-missing']))

    def test_lifecycle_cannot_coexist_with_destination(self):
        code, violations = self.violations(['Needs-Triage', 'AFK'])
        self.assertEqual((code, violations), (1, ['lifecycle.needs-triage-with-destination']))
        code, violations = self.violations(['Needs-Info', 'HITL', 'HITL:Research'])
        self.assertEqual((code, violations), (1, ['lifecycle.needs-info-with-destination']))

    def test_hitl_blocked_is_allowed(self):
        code, violations = self.violations(['HITL', 'HITL:Decision', 'Blocked:External'])
        self.assertEqual((code, violations), (0, []))

    def test_closed_issue_skips_destination_rules(self):
        # close 済みの destination は履歴なので、書き換えも失敗もさせない。
        code, out = evaluate('closed', CATEGORY + ['AFK', 'HITL'])
        self.assertEqual((code, out), (0, {'add': [], 'remove': [], 'violations': []}))


if __name__ == '__main__':
    unittest.main()
