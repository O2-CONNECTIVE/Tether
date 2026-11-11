import pathlib
import tempfile
import unittest
from package_local import package


class PackageChecks(unittest.TestCase):
    def test_fixed_layout_no_overwrite(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            gui, resident = root / "gui", root / "resident"
            gui.write_bytes(b"synthetic-gui")
            resident.write_bytes(b"synthetic-resident")
            output = root / "Development.app"
            package(gui, resident, output)
            self.assertEqual((output / "Contents/MacOS/tether-dev-resident").read_bytes(), resident.read_bytes())
            self.assertEqual(len((output / "Contents/Resources/resident.sha256").read_text().strip()), 64)
            with self.assertRaises(ValueError):
                package(gui, resident, output)

    def test_symlink_input_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            actual, link = root / "actual", root / "link"
            actual.write_bytes(b"synthetic")
            link.symlink_to(actual)
            with self.assertRaises(ValueError):
                package(link, actual, root / "out.app")
            self.assertFalse((root / "out.app").exists())


if __name__ == "__main__":
    unittest.main()
