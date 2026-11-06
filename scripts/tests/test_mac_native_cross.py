import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('native_cross', SCRIPTS / 'check_mac_native_cross.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class NativeCrossPrerequisitesTests(unittest.TestCase):
    def test_missing_or_empty_target_std_is_not_compile_success(self):
        with tempfile.TemporaryDirectory() as temporary:
            for directory in (Path(temporary), Path(temporary) / 'absent'):
                with self.subTest(directory=directory), self.assertRaises(ValueError):
                    tool.require_intel_std(str(directory))

    def test_wrong_image_architecture_is_rejected(self):
        for output in ('arm64\n', 'x86_64 arm64\n', ''):
            with self.subTest(output=output), patch.object(tool, 'command', return_value=output):
                with self.assertRaises(ValueError):
                    tool.require_intel_image(Path('owned-fixture'), {})
        with patch.object(tool, 'command', return_value='x86_64\n'):
            tool.require_intel_image(Path('owned-fixture'), {})

    def test_non_mac_refuses_before_compiler(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as command:
            with self.assertRaises(ValueError):
                tool.run()
        command.assert_not_called()
