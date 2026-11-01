import importlib.util
from pathlib import Path
import plistlib
import tempfile
import struct
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("packaging_local", ROOT/"scripts/package_local.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class PackagingTests(unittest.TestCase):
    def fixture(self, root):
        (root/".build/arm64-apple-macosx/debug").mkdir(parents=True)
        (root/".build/arm64-apple-macosx/debug/TetherStatus").write_bytes(struct.pack("<8I",0xfeedfacf,0x0100000c,0,2,1,24,0,0)+struct.pack("<6I",0x32,24,1,0x000d0000,0,0))
        (root/"Packaging").mkdir()
        (root/"Packaging/Info.plist").write_bytes((ROOT/"Packaging/Info.plist").read_bytes())

    def test_layout_and_notice_no_extra_payload(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root);dest=module.package(root)
            self.assertEqual(sorted(str(p.relative_to(dest)) for p in dest.rglob("*") if p.is_file()),
                ["Contents/Info.plist","Contents/MacOS/TetherStatus","Contents/Resources/LOCAL-EVALUATION.txt"])
            self.assertIn("Do not distribute", (dest/"Contents/Resources/LOCAL-EVALUATION.txt").read_text())

    def test_existing_bundle_refused_without_change(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root);dest=module.package(root)
            binary=dest/"Contents/MacOS/TetherStatus";before=binary.read_bytes()
            with self.assertRaises(FileExistsError):module.package(root)
            self.assertEqual(before,binary.read_bytes())

    def test_wrong_binary_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root)
            (root/".build/arm64-apple-macosx/debug/TetherStatus").write_bytes(b"not-mach-o")
            with self.assertRaises(ValueError):module.package(root)

    def test_external_binary_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root)
            src=root/".build/arm64-apple-macosx/debug/TetherStatus";src.unlink()
            outside=root/"outside";outside.write_bytes(b"\xcf\xfa\xed\xfe")
            src.symlink_to(outside)
            with self.assertRaises((ValueError,OSError)):module.package(root)

    def test_existing_manifest_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root)
            parent=root/".build/local-evaluation";parent.mkdir()
            manifest=parent/"bundle-manifest.json";manifest.write_text("preserve")
            with self.assertRaises(FileExistsError):module.package(root)
            self.assertEqual(manifest.read_text(),"preserve")
            self.assertFalse((parent/"Tether Status.app").exists())

    def test_unexpected_plist_field_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root)
            path=root/"Packaging/Info.plist";p=plistlib.loads(path.read_bytes())
            p["CFBundleURLTypes"]=[];path.write_bytes(plistlib.dumps(p))
            with self.assertRaises(ValueError):module.package(root)

    def test_binary_minimum_mismatch_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root)
            path=root/".build/arm64-apple-macosx/debug/TetherStatus"
            data=bytearray(path.read_bytes());struct.pack_into("<I",data,44,0x000e0000);path.write_bytes(data)
            with self.assertRaises(ValueError):module.package(root)

    def test_misaligned_load_command_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root)
            p=root/".build/arm64-apple-macosx/debug/TetherStatus"
            data=bytearray(p.read_bytes());struct.pack_into("<I",data,36,23);p.write_bytes(data)
            with self.assertRaises(ValueError):module.package(root)

    def test_inconsistent_tool_table_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root)
            p=root/".build/arm64-apple-macosx/debug/TetherStatus"
            data=bytearray(p.read_bytes());struct.pack_into("<I",data,52,1);p.write_bytes(data)
            with self.assertRaises(ValueError):module.package(root)

    def test_plist_minimum_and_no_privilege_keys(self):
        p=plistlib.loads((ROOT/"Packaging/Info.plist").read_bytes())
        self.assertEqual(p["LSMinimumSystemVersion"],"13.0")
        self.assertNotIn("SMPrivilegedExecutables",p)
        self.assertNotIn("NSAppleEventsUsageDescription",p)

if __name__=="__main__":unittest.main()
