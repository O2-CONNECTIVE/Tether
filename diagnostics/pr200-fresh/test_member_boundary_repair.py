"""Source contracts/retained facts only. PowerShell regressions and controls remain unrun locally."""
import copy
import hashlib
import json
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parent
BASE_HASH = '474dd092d55e9057938dffe15dbeb5dba28dbe51a3e510c01e4281a5757c463f'
GETTER_BEFORE = "@('true', 'value', $true, 'metadata-command-line-read-exception', 1, 'getter-exception')"
GETTER_AFTER = "@('true', 'value', $true, 'metadata-command-line-guard', 1, 'getter-exception')"


def without_boundary_repair(text):
    for region in ('FUNCTION', 'CHECK', 'CONTROLS'):
        text = re.sub(r'(?m)^# BEGIN_MEMBER_BOUNDARY_' + region + r'\n.*?^# END_MEMBER_BOUNDARY_' + region + r'\n', '', text, flags=re.S)
    text = text.replace('    # Creation-error only: preserve the directly thrown sentinel reference.\n    # This does not assert ScriptProperty getter exception identity.',
        '    # Getter exceptions may be wrapped by PowerShell; the original inner cause\n    # must retain reference identity, never replaced by classification machinery.')
    return text.replace(GETTER_AFTER, GETTER_BEFORE)


def repair_contract(text):
    # Deliberately static: detects known source regressions, never simulates PS behavior.
    required = [GETTER_AFTER, 'function Test-MemberBoundaryOutcome',
        "$shouldReject = $CaseId -cne 'valid-command'",
        '($null -ne $Old.caught) -ne $shouldReject', '($null -ne $New.caught) -ne $shouldReject',
        "$Old.caught.Exception.Message -cne 'PROFILE_IDENTITY_UNKNOWN'",
        "$New.caught.Exception.Message -cne 'PROFILE_IDENTITY_UNKNOWN'",
        '$boundaryFailure = Test-MemberBoundaryOutcome $old $new $case[5]',
        "Test-MemberBoundaryOutcome $accepted $accepted 'empty-command'",
        "Test-MemberBoundaryOutcome $rejected $rejected 'valid-command'",
        "Test-MemberBoundaryOutcome $wrongError $wrongError 'getter-exception'",
        "'METADATA_BOUNDARY_CONTROL_FAILED'"]
    return all(part in text for part in required) and GETTER_BEFORE not in text


class MemberBoundaryRepairTests(unittest.TestCase):
    def test_source_repair_requires_exact_member_boundary_and_absolute_acceptance(self):
        self.assertTrue(repair_contract((ROOT / 'observer-detail.test.ps1').read_text()))

    def test_static_negative_mutants_do_not_satisfy_repair_contract(self):
        text = (ROOT / 'observer-detail.test.ps1').read_text()
        for old, new in [(GETTER_AFTER, GETTER_BEFORE),
            ('($null -ne $Old.caught) -ne $shouldReject', '$false'),
            ("Test-MemberBoundaryOutcome $rejected $rejected 'valid-command'", "'pass'"),
            ("$Old.caught.Exception.Message -cne 'PROFILE_IDENTITY_UNKNOWN'", '$false')]:
            self.assertFalse(repair_contract(text.replace(old, new)))

    def test_source_projection_preserves_all_other_fixture_logic(self):
        text = (ROOT / 'observer-detail.test.ps1').read_text()
        self.assertEqual(hashlib.sha256(without_boundary_repair(text).encode()).hexdigest(), BASE_HASH)
        self.assertIn("$before = { if (-not (Test-LineageCreation $view) -or -not $view.CommandLine)", text)
        self.assertIn('$view | Add-Member ScriptProperty CommandLine', text)
        self.assertIn('if ($script:commandError) { throw $script:sentinel }', text)
        self.assertNotIn('GetterScript.Invoke', text)
        self.assertNotIn("Properties['CommandLine'].Value", text)

    def test_real_pr217_boundary_failure_is_preserved_without_patched_runtime_claim(self):
        record = json.loads((ROOT / 'fixtures/pr217-fixture-record.json').read_text())
        original = copy.deepcopy(record)
        canonical = json.dumps(record, sort_keys=True, separators=(',', ':')).encode()
        self.assertEqual(len(canonical), 2084)
        self.assertEqual(hashlib.sha256(canonical).hexdigest(), 'e91afcf1b623f91c78cbe1adeb689bc31894b7318e5e80c915cadd9c519de6b4')
        self.assertEqual(len(record['files']), 2)
        for name, value in record['files'].items():
            self.assertEqual(hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest(), record['fileSha256'][name])
        observed = record['files']['powershell-preflight.json']
        self.assertFalse(observed['passed'])
        self.assertEqual(observed['detailAssertion']['actualDetail'], 'metadata-command-line-guard')
        self.assertEqual(record, original)


if __name__ == '__main__':
    unittest.main()
