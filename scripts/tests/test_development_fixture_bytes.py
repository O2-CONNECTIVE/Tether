"""Public contract bytes must survive Git's Windows-style checkout conversion."""
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = 'ui/mac-development/Tests/DevelopmentChecks/Fixtures/mapper-pending.jsonl'


class DevelopmentFixtureCheckoutTests(unittest.TestCase):
    def test_autocrlf_checkout_preserves_exact_mapper_bytes(self):
        expected = (ROOT / FIXTURE).read_bytes()
        self.assertNotIn(b'\r', expected)
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            def git(*args):
                return subprocess.run(
                    ['git', '-C', str(repo), '-c', 'user.name=Fixture',
                     '-c', 'user.email=fixture@example.invalid', *args],
                    check=True, capture_output=True)
            git('init', '-q')
            (repo / '.gitattributes').write_bytes((ROOT / '.gitattributes').read_bytes())
            target = repo / FIXTURE
            target.parent.mkdir(parents=True)
            target.write_bytes(expected)
            git('-c', 'core.autocrlf=false', 'add', '.')
            git('-c', 'core.autocrlf=false', 'commit', '-qm', 'fixture')
            target.unlink()  # Only this owned temporary fixture, never user data.
            git('-c', 'core.autocrlf=true', 'checkout', 'HEAD', '--', FIXTURE)
            actual = target.read_bytes()
            self.assertEqual(actual, expected, 'checkout changed wire fixture bytes')


if __name__ == '__main__':
    unittest.main()
