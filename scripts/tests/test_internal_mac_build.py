"""Synthetic bytes only: no executable, identity, enrollment or network."""
import contextlib
import hashlib
import io
import json
from pathlib import Path
import plistlib
import tempfile
import unittest

from scripts import check_internal_mac_build as checker


class InternalBuildChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.app = self.root / "Development.app"
        for directory in checker.DIRECTORIES:
            (self.app / directory).mkdir(parents=True, exist_ok=True)
        data = {"gui": b"synthetic gui", "resident": b"synthetic resident",
                "plist": plistlib.dumps({"CFBundleIdentifier": "com.o2connective.tether.development.gui",
                                         "CFBundleExecutable": "TetherDevelopment"})}
        data["resident_digest"] = (hashlib.sha256(data["resident"]).hexdigest() + "\n").encode()
        for role, content in data.items():
            (self.app / checker.FILES[role]).write_bytes(content)
        self.source = "a" * 40
        self.manifest = {"schema": checker.SCHEMA, "source_commit": self.source,
                         "profile": "development-local-only",
                         "sha256": {role: hashlib.sha256(content).hexdigest() for role, content in data.items()}}
        self.record = self.root / "record.json"
        self.save()

    def save(self):
        self.record.write_text(json.dumps(self.manifest))

    def inspect(self):
        return checker.inspect(self.app, self.record, self.source)

    def rejected(self):
        with self.assertRaises((ValueError, OSError)):
            self.inspect()

    def test_matching_bytes_never_authorize(self):
        result = self.inspect()
        self.assertEqual(result["source_binary_provenance"], "not-established")
        for flag in ("launch_authorized", "publisher_verified", "production_ready", "distribution_authorized"):
            self.assertIs(result[flag], False)

    def test_source_mismatch(self):
        self.manifest["source_commit"] = "b" * 40
        self.save()
        self.rejected()

    def test_product_profile_refused(self):
        self.manifest["profile"] = "production"
        self.save()
        self.rejected()

    def test_changed_each_role(self):
        for role in checker.FILES:
            with self.subTest(role=role):
                target = self.app / checker.FILES[role]
                original = target.read_bytes()
                target.write_bytes(original + b"changed")
                self.rejected()
                target.write_bytes(original)

    def test_unknown_file(self):
        (self.app / "secret.txt").write_text("not a real secret")
        self.rejected()

    def test_unknown_directory(self):
        (self.app / "extra").mkdir()
        self.rejected()

    def test_missing_file(self):
        (self.app / checker.FILES["gui"]).unlink()
        self.rejected()

    def test_wrong_app_even_with_matching_hash(self):
        target = self.app / checker.FILES["plist"]
        target.write_bytes(plistlib.dumps({"CFBundleIdentifier": "production"}))
        self.manifest["sha256"]["plist"] = hashlib.sha256(target.read_bytes()).hexdigest()
        self.save()
        self.rejected()

    def test_resident_digest_not_self_authorizing(self):
        target = self.app / checker.FILES["resident_digest"]
        target.write_bytes(b"0" * 64 + b"\n")
        self.manifest["sha256"]["resident_digest"] = hashlib.sha256(target.read_bytes()).hexdigest()
        self.save()
        self.rejected()

    def test_duplicate_record_key(self):
        self.record.write_text('{"schema":"a","schema":"b"}')
        self.rejected()

    def test_unknown_record_key(self):
        self.manifest["approved"] = True
        self.save()
        self.rejected()

    def test_role_swap(self):
        hashes = self.manifest["sha256"]
        hashes["gui"], hashes["resident"] = hashes["resident"], hashes["gui"]
        self.save()
        self.rejected()

    def test_symlink(self):
        target = self.app / checker.FILES["gui"]
        target.unlink()
        try:
            target.symlink_to(self.record)
        except OSError:
            self.skipTest("symlink creation not permitted")
        self.rejected()

    def test_symlink_ancestor(self):
        alias = self.root / "alias"
        try:
            alias.symlink_to(self.app, target_is_directory=True)
        except OSError:
            self.skipTest("symlink creation not permitted")
        self.app = alias
        self.rejected()

    def test_cli_refusal_no_path_leak(self):
        self.record.write_text("invalid")
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = checker.main(["--app", str(self.app), "--record", str(self.record), "--source-commit", self.source])
        self.assertEqual(code, 1)
        self.assertEqual(out.getvalue(), "")
        self.assertEqual(err.getvalue(), "preflight-refused\n")

    def test_empty_binary(self):
        (self.app / checker.FILES["gui"]).write_bytes(b"")
        self.rejected()


if __name__ == "__main__":
    unittest.main()
