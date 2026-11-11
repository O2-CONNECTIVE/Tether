"""Static diagnostic invariants, not native test evidence."""
from pathlib import Path
import re
import subprocess
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
BASE = '3b0ec99a367b71d4edfb31ddfd479e7740b30e6c'


def unwrap(text):
    pattern = r'Diagnostic\.(?:Api|Handle|Resume|Wait)\('
    while match := re.search(pattern, text):
        start = match.end()
        end = start
        depth = 1
        first_end = None
        while depth:
            char = text[end]
            if char == '(':
                depth += 1
            elif char == ')':
                depth -= 1
            elif char == ',' and depth == 1 and first_end is None:
                first_end = end
            end += 1
        text = text[:match.start()] + text[start:first_end or end - 1] + text[end:]
    return text


def calls(text):
    result = []
    for match in re.finditer(r'(?:Native|DescendantNative|FixtureNative)\.(?!Require\b)\w+\(', text):
        end, depth = match.end(), 1
        while depth:
            if text[end] == '(':
                depth += 1
            elif text[end] == ')':
                depth -= 1
            end += 1
        result.append(re.sub(r'\s+', '', text[match.start():end]))
    return result


class DiagnosticSource(unittest.TestCase):
    def test_same_native_calls_arguments_and_order(self):
        for name in ['AppOwner.cs', 'FixtureNative.cs', 'Program.cs']:
            original = subprocess.check_output(['git', '-C', str(ROOT), 'show', BASE + ':tools/windows-app-owner/' + name], text=True)
            self.assertEqual(calls(original), calls(unwrap((HERE / name).read_text())), name)

    def test_same_close_assertions_and_budgets(self):
        owner = unwrap((HERE / 'AppOwner.cs').read_text())
        for assertion in ['Active() == 1', 'timer.ElapsedMilliseconds < 5000', 'RootExited(0)', 'code == expected',
                          'result != Outcome.NormalExit || (owner.normal && !owner.forced)',
                          'result == Outcome.NormalExit || (!owner.normal && owner.forced)']:
            self.assertIn(assertion, owner)

    def test_primary_cleanup_separate(self):
        owner = (HERE / 'AppOwner.cs').read_text()
        self.assertIn('Diagnostic.Primary = Diagnostic.Capture(error)', owner)
        self.assertIn('Diagnostic.Cleanup = Diagnostic.Capture(error)', owner)
        self.assertIn('if (Diagnostic.Primary != null) throw Diagnostic.Primary', owner)
        self.assertIn('if (Diagnostic.Cleanup != null) throw Diagnostic.Cleanup', owner)

    def test_no_actual_app_continuation(self):
        workflow = (ROOT / '.github/workflows/windows-owner-prerequisite-diagnostic.yml').read_text()
        for forbidden in ['setup.ps1', 'build.ps1', 'run.ps1', 'cargo ', 'npm ', 'attach.ts']:
            self.assertNotIn(forbidden, workflow)
        script = (HERE / 'diagnostic.test.ps1').read_text()
        self.assertIn("actualAppExecuted = $false; actualAppGate = 'closed'", script)
        self.assertIn('firstFailureRun = 42967354064', script)


if __name__ == '__main__':
    unittest.main()
