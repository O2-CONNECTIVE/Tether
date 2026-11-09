"""Source/allowlist contracts only; never launch a native process."""
import re
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[5]
PHASES = ('init', 'on', 'auth', 'off', 'complete')
REASONS = ('identity', 'receipt', 'transition', 'retry-budget', 'phase-timeout',
           'auth-timeout', 'bridge-timeout', 'unexpected')


class DiagnosticContract(unittest.TestCase):
    def test_workflow_projects_only_closed_enum_lines(self):
        workflow = (ROOT / '.github/workflows/mac-resident-gui.yml').read_text()
        pattern = re.search(r"grep -E '([^']*GUI_FIXTURE_FAILURE[^']*)'", workflow).group(1)
        for phase in PHASES:
            for reason in REASONS:
                self.assertRegex(f'GUI_FIXTURE_FAILURE={phase}:{reason}', pattern)
        for value in ('event:sequence', 'event:lock'):
            self.assertRegex(f'GUI_FIXTURE_FAILURE={value}', pattern)
        for value in ('on:identity:extra', 'on:https://secret.invalid', 'private:identity',
                      'on:identity\nsecret', 'off:unexpected\r', 'event:payload'):
            self.assertIsNone(re.fullmatch(pattern, f'GUI_FIXTURE_FAILURE={value}'))

    def test_rust_failure_remains_terminal_and_lifecycle_unchanged(self):
        rust = (ROOT / 'ui/tauri/src-tauri/src/resident_gui_fixture.rs').read_text()
        projection = rust.split('fn failure_class(', 1)[1].split('fn hex(', 1)[0]
        for token in PHASES + REASONS:
            self.assertIn(f'"{token}" => "{token}"', projection)
        self.assertIn('eprintln!("GUI_FIXTURE_FAILURE={phase}:{reason}")', projection)
        self.assertNotIn('eprintln!("{payload}', rust)
        self.assertIn('*state = 5;\n    "driver-failed"', rust)
        self.assertIn('result.store(2, Ordering::SeqCst)', rust)
        self.assertIn('owner.gate.outcome() == Some(Outcome::Confirmed)', rust)
        self.assertIn('std::hint::black_box(&owner);\n        retain();', rust)
        self.assertIn('owner.join(); // exact producer IO/Wait AND original outer monitor', rust)

    def test_driver_has_no_raw_error_projection_or_new_retry(self):
        driver = (ROOT / 'ui/tauri/src-tauri/tests/resident-gui/drive.js').read_text()
        self.assertIn('await emit(`failed:${stage}:${reason}`)', driver)
        self.assertIn('} catch {', driver)
        self.assertNotIn('.message', driver)
        self.assertNotIn('.stack', driver)
        self.assertEqual(driver.count('performance.now() + 20000'), 3)
        self.assertEqual(driver.count('button.click()'), 1)
        self.assertIn("if (++attempts > 8) fail('retry-budget')", driver)
        self.assertIn("received && receipt === 'stale' && revision > sentRevision", driver)


if __name__ == '__main__':
    unittest.main()
