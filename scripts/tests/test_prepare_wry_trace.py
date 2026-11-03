"""Offline preparation/identity fixtures; no Cargo, Rust build, or live WebView."""
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("prepare_wry_trace", ROOT / "scripts/prepare_wry_trace.py")
p = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(p)


def lock_text():
    return ('version = 4\n\n[[package]]\nname = "rescue-host-shell"\nversion = "0.1.0"\n'
            'dependencies = [\n "tauri",\n]\n\n[[package]]\nname = "wry"\n'
            'version = "0.57.1"\nsource = "registry+https://github.com/rust-lang/crates.io-index"\n'
            'checksum = "' + p.ARCHIVE_SHA256 + '"\ndependencies = [\n "other",\n]\n\n'
            '[[package]]\nname = "other"\nversion = "1.2.3"\nchecksum = "unchanged"\n')


def app_files():
    return {"src-tauri/Cargo.toml": b"[package]\nname = \"rescue-host-shell\"\n"
            b"[target.'cfg(windows)'.dependencies]\n",
            "src-tauri/Cargo.lock": lock_text().encode(),
            "src-tauri/src/startup_diagnostics.rs":
            b"wry_hook: AtomicBool\n        let recorder = Recorder::memory();\n",
            "src-tauri/tests/control_contract.rs":
            b'include_str!("../../../../contracts/control-v1/vectors.json");\n' * 3,
            "src-tauri/tests/control_coordinator.rs":
            b'include_str!("../../../../contracts/control-v1/vectors.json");\n',
            "src-tauri/src/lib.rs": b'include_str!("../../contracts/shell-status-v1.json");',
            "contracts/control-v1/vectors.json": b'{"fixture": "control"}',
            "contracts/shell-status-v1.json": b'{"fixture": "status"}',
            "dist/index.html": b"<html></html>"}


class PreparationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.source = self.root / "source"
        self.source.mkdir()
        self.output = self.root / "output"
        self.bundle = ROOT / "tools/wry-startup-trace"

    def test_lock_only_two_packages_changed(self):
        before = lock_text().split("[[package]]\n")
        after = p.prepare_lock(lock_text()).split("[[package]]\n")
        self.assertEqual(before[0], after[0])
        self.assertEqual(before[3:], after[3:])
        self.assertEqual(after[1], before[1].replace('\n]\n', '\n "wry",\n]\n'))
        self.assertEqual(after[2], before[2].replace(
            'source = "registry+https://github.com/rust-lang/crates.io-index"\n', '').replace(
            'checksum = "' + p.ARCHIVE_SHA256 + '"\n', ''))

    def test_lock_rejects_version_checksum_duplicate_or_existing_edge(self):
        bad = [lock_text().replace('0.57.1', '0.57.2'),
               lock_text().replace(p.ARCHIVE_SHA256, "0" * 64),
               lock_text() + '[[package]]\nname = "wry"\n',
               lock_text().replace(' "tauri",', ' "tauri",\n "wry",')]
        for text in bad:
            with self.subTest(text=text), self.assertRaises(ValueError):
                p.prepare_lock(text)

    def test_app_bridge_and_windows_line_endings(self):
        files = {name: data.replace(b"\n", b"\r\n") for name, data in app_files().items()}
        prepared = p.prepare_app(files, (self.bundle / "app_hook.rs.txt").read_bytes())
        manifest = prepared["src-tauri/Cargo.toml"]
        self.assertIn(b'default-features = false', manifest)
        self.assertIn(b'path = "../../wry"', manifest)
        self.assertNotIn(b"\r\n", prepared["src-tauri/Cargo.lock"])
        self.assertIn(b"recorder.wry_hook.store(true, SeqCst)", prepared[
            "src-tauri/src/startup_diagnostics.rs"])
        self.assertEqual(files["dist/index.html"], prepared["dist/index.html"])

    def test_all_generated_includes_resolve_inside_app_without_mutating_inputs(self):
        files = app_files()
        original = dict(files)
        result = p.prepare_app(files, (self.bundle / "app_hook.rs.txt").read_bytes())
        self.assertEqual(files, original)
        self.assertEqual(result["contracts/control-v1/vectors.json"],
                         original["contracts/control-v1/vectors.json"])
        self.assertEqual(result["contracts/shell-status-v1.json"],
                         original["contracts/shell-status-v1.json"])
        p.verify_includes(result)
        for name in ("src-tauri/tests/control_contract.rs",
                     "src-tauri/tests/control_coordinator.rs"):
            self.assertNotIn(b"../../../../contracts", result[name])
            self.assertIn(b"../../contracts/control-v1/vectors.json", result[name])

    def test_include_closure_rejects_escape_missing_and_dynamic_targets(self):
        for target in ('"../../../../escape.json"', '"missing.json"', 'env!("INPUT")'):
            files = app_files()
            files["src-tauri/src/new.rs"] = ("include_str!(" + target + ");").encode()
            with self.subTest(target=target), self.assertRaises(ValueError):
                p.prepare_app(files, (self.bundle / "app_hook.rs.txt").read_bytes())

    def test_known_contract_include_count_drift_is_refused(self):
        files = app_files()
        files["src-tauri/tests/control_contract.rs"] = b"no includes"
        with self.assertRaisesRegex(ValueError, "preimage"):
            p.prepare_app(files, (self.bundle / "app_hook.rs.txt").read_bytes())

    def test_app_missing_or_duplicate_marker_refused(self):
        for trace in (b"missing", app_files()["src-tauri/src/startup_diagnostics.rs"] * 2):
            files = app_files()
            files["src-tauri/src/startup_diagnostics.rs"] = trace
            with self.assertRaises(ValueError):
                p.prepare_app(files, b"hook")

    def test_bad_archive_hash_before_output(self):
        archive = self.root / "bad.crate"
        archive.write_bytes(b"wrong")
        with self.assertRaisesRegex(ValueError, "checksum"):
            p.prepare(self.source, archive, self.output)
        self.assertFalse(self.output.exists())

    def test_cli_rejection_does_not_print_raw_inputs(self):
        archive = self.root / "SECRET_PAYLOAD.crate"
        archive.write_bytes(b"PRIVATE_CONTENT")
        result = subprocess.run([sys.executable, str(ROOT / "scripts/prepare_wry_trace.py"),
                                 "--source-root", str(self.source), "--archive", str(archive),
                                 "--output", str(self.output)], stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, b"")
        self.assertEqual(result.stderr, b"WRY_PREPARATION_REJECTED" + os.linesep.encode("ascii"))
        self.assertFalse(self.output.exists())

    def archive_fixture(self, entries):
        stream = io.BytesIO()
        with tarfile.open(fileobj=stream, mode="w:gz") as archive:
            for name, kind in entries:
                info = tarfile.TarInfo(name)
                info.type = kind
                info.linkname = "../escape" if kind == tarfile.SYMTYPE else ""
                info.size = 0
                archive.addfile(info, io.BytesIO())
        data = stream.getvalue()
        archive = self.root / "fixture.crate"
        archive.write_bytes(data)
        return archive, p.digest(data)

    def test_archive_rejects_traversal_link_and_duplicate(self):
        for entries in [
            [("wry-0.57.1/../escape", tarfile.REGTYPE)],
            [("wry-0.57.1/link", tarfile.SYMTYPE)],
            [("wry-0.57.1/a", tarfile.REGTYPE)] * 2,
        ]:
            archive, checksum = self.archive_fixture(entries)
            with patch.object(p, "ARCHIVE_SHA256", checksum), self.assertRaises(ValueError):
                p.read_archive(archive)

    def test_bundle_mutation_refused_even_if_self_reported_hash_updated(self):
        original = {name: (self.bundle / name).read_bytes() for name in p.BUNDLE_FILES}
        for name in p.BUNDLE_FILES:
            mutated = dict(original)
            mutated[name] += b" "
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "bundle checksum"):
                p.apply_patch({}, mutated)
        mutated = dict(original)
        spec = json.loads(mutated["patch.json"])
        spec["files"][0]["beforeSha256"] = p.digest(b"malicious")
        spec["files"][0]["afterSha256"] = p.digest(b"malicious")
        mutated["patch.json"] = json.dumps(spec).encode()
        with self.assertRaisesRegex(ValueError, "bundle checksum"):
            p.apply_patch({"src/lib.rs": b"malicious"}, mutated)

    def test_patch_preimage_refused(self):
        bundle = {name: (self.bundle / name).read_bytes() for name in p.BUNDLE_FILES}
        with self.assertRaisesRegex(ValueError, "preimage"):
            p.apply_patch({"src/lib.rs": b"modified"}, bundle)

    def test_existing_or_nested_output_refused(self):
        self.output.mkdir()
        for out in (self.output, self.source / "nested", self.source):
            with self.assertRaises(ValueError):
                p.prepare(self.source, self.root / "unused", out)

    def test_source_symlink_refused(self):
        target = self.root / "target"
        target.write_bytes(b"data")
        link = self.root / "link"
        try:
            link.symlink_to(target)
        except OSError:
            self.skipTest("symlink privilege unavailable")
        with self.assertRaises(ValueError):
            p.read_regular(link)

    def test_collect_only_tracked_source_and_generated_dist(self):
        tracked = "ui/tauri/src-tauri/src/main.rs"
        path = self.source / tracked
        path.parent.mkdir(parents=True)
        path.write_bytes(b"source")
        (path.parent / "private.rs").write_bytes(b"never copy")
        dist = self.source / "ui/tauri/dist"
        dist.mkdir()
        (dist / "index.html").write_bytes(b"generated")
        for name in p.CONTRACTS:
            path = self.source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"contract fixture")
        fake = subprocess.CompletedProcess([], 0,
            ("\0".join([tracked, *p.CONTRACTS]) + "\0").encode())
        with patch.object(p.subprocess, "run", return_value=fake):
            files = p.collect_app(self.source)
            self.assertEqual(set(files), {"src-tauri/src/main.rs", "dist/index.html",
                                          *p.CONTRACTS.values()})
            (dist / ".secret").write_bytes(b"no")
            with self.assertRaises(ValueError):
                p.collect_app(self.source)

    def prepare_fixture(self):
        with patch.object(p, "read_archive", return_value={}), \
             patch.object(p, "apply_patch", return_value={"Cargo.toml": b"wry"}), \
             patch.object(p, "collect_app", return_value=app_files()):
            return p.prepare(self.source, self.root / "unused", self.output)

    def test_preparation_verification_and_source_unchanged(self):
        before = list(self.source.iterdir())
        provenance = self.prepare_fixture()
        self.assertEqual(p.verify_output(self.output), provenance)
        self.assertEqual(list(self.source.iterdir()), before)
        self.assertEqual(provenance["generatedLockSha256"],
                         p.digest((self.output / "app/src-tauri/Cargo.lock").read_bytes()))
        (self.output / "app/src-tauri/Cargo.lock").write_bytes(b"changed")
        with self.assertRaises(ValueError):
            p.verify_output(self.output)

    def test_extra_rust_refused_generated_json_allowed(self):
        self.prepare_fixture()
        gen = self.output / "app/src-tauri/gen"
        gen.mkdir()
        (gen / "schema.json").write_text("{}")
        p.verify_output(self.output)
        (gen / "unexpected.rs").write_text("unsafe")
        with self.assertRaises(ValueError):
            p.verify_output(self.output)

    def graph(self):
        packages = [
            {"name": "wry", "id": "wry-id", "version": "0.57.1", "source": None,
             "manifest_path": str(self.output / "wry/Cargo.toml")},
            {"name": "rescue-host-shell", "id": "app-id",
             "manifest_path": str(self.output / "app/src-tauri/Cargo.toml")},
            {"name": "tauri-runtime-wry", "id": "runtime-id"},
        ]
        return {"packages": packages, "resolve": {"root": "app-id", "nodes": [
            {"id": "app-id", "dependencies": ["wry-id", "runtime-id"]},
            {"id": "runtime-id", "dependencies": ["wry-id"]}]}}

    def test_graph_requires_same_path_instance_for_both_consumers(self):
        p.verify_graph(self.graph(), self.output)
        for mutate in (
            lambda g: g["packages"].append(copy.deepcopy(g["packages"][0])),
            lambda g: g["packages"][0].update(source="registry"),
            lambda g: g["packages"][0].update(version="0.57.2"),
            lambda g: g["packages"][0].update(manifest_path="/wrong/Cargo.toml"),
            lambda g: g["resolve"]["nodes"][0].update(dependencies=["different-wry"]),
            lambda g: g["resolve"]["nodes"][1].update(dependencies=["different-wry"]),
        ):
            data = self.graph()
            mutate(data)
            with self.assertRaises(ValueError):
                p.verify_graph(data, self.output)


if __name__ == "__main__":
    unittest.main()
