from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


prepare = load("prepare_wry_probe")
record = load("wry_probe_record")
ci = load("wry_probe_ci")


def example(stages=None):
    stages = stages if stages is not None else record.STAGES
    rows = [{"seq": n + 1, "stage": s, "elapsedUs": n + 1, "onTraceThread": True}
            for n, s in enumerate(stages)]
    return dict(schema=1, variant="wry-completion-probe-1", purpose="diagnostic-hints-only",
                registered=True, sealed=True, sealElapsedUs=20, attempted=len(rows),
                recorded=len(rows), dropped=0, inFlight=0, events=rows)


class RecordTests(unittest.TestCase):
    def test_atomic_checkpoint_failure_preserves_previous_prefix(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "steps.json"
            original = [{"stage": "pins", "exitCode": 0}]
            ci.checkpoint(path, original)
            with patch.object(ci.os, "replace", side_effect=OSError("PRIVATE_SENTINEL")):
                with self.assertRaises(OSError): ci.checkpoint(path, original + [{"stage": "rust", "exitCode": 1}])
            self.assertEqual(ci.load(path), original)
            self.assertEqual([p.name for p in path.parent.iterdir()], ["steps.json"])

    def test_actual_publisher_requires_all_critical_sections_and_handles_bad_host_rows(self):
        # Synthetic complete record: proves publisher policy, not Windows/runtime success.
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory); (temp / "evidence").mkdir(); (temp / "tools/app-startup").mkdir(parents=True); (temp / "unpacked").mkdir()
            def write(name, value):
                (temp / name).write_bytes(ci.canonical(value))
            write("steps.json", [{"stage": name, "exitCode": 0} for name in ci.STEPS])
            write("evidence/native-result.json", {"nativeTestsRun": 1, "nativeTestsPassed": 1, "normalCloseVerified": True,
                "cleanupStarted": True, "cleanupFailures": 0, "childHandleDisposeFailures": 0,
                "driverExitObservedAfterCleanup": True, "appStartupFileClosed": True})
            observation = {"status": "webview-passed-awaiting-close", "phase": "complete", "elapsedMs": 100,
                           "documentObservations": 1, "droppedRequests": 0, "errorCode": "",
                           "requests": [{"operation": "create-session", "outcome": "started", "elapsedMs": 0},
                                        {"operation": "create-session", "outcome": "ok", "elapsedMs": 80, "status": 200}]}
            write("evidence/native-observation.json", observation)
            # Preserve Rust writer's canonical field order, checked by read_record.
            (temp / "tools/app-startup/wry-completion-probe.json").write_text(json.dumps(example(), separators=(",", ":")))
            meta = {key: "a" * 64 for key in ("overlayLockSha256", "vendorTreeSha256", "exeSha256", "driverSha256", "archiveSha256", "edgeDriverSha256", "driverPatchSha256", "driverLockSha256")}
            meta.update(sourceCommit=ci.git(ROOT, "rev-parse", "HEAD").decode().strip(), sourceTree=ci.git(ROOT, "rev-parse", "HEAD^{tree}").decode().strip())
            write("unpacked/BUILD.json", {"schema": 1, "variant": "host-startup-diagnostic-1", "sourceCommit": meta["sourceCommit"],
                "features": ["custom-protocol", "startup-diagnostics"], "profile": "release", "exeSha256": "a" * 64, "distribution": False})
            write("launch.json", meta); write("final.json", meta)
            overlay = {"schema": 1, "variant": "wry-completion-overlay-1", "files": 84,
                       "crateSha256": prepare.CRATE_SHA, "originalLockSha256": prepare.LOCK_SHA,
                       "originalSha256": "9ef674af12e52bc3519b0e300752c1042798b7f336e0e0dafca6a8944f348888",
                       "patchedSha256": "8d4a637727ce94dab6c6fb8e676dde8f2e7a4d0c10681fef0b82dbda42654700", "overlayLockSha256": "a" * 64}
            write("overlay.json", overlay)
            host = {"diagnostic": "complete", "startup": {"phase": "first-page-load-finished", "events": [
                {"seq": i + 1, "stage": stage, "elapsedUs": i + 1} for i, stage in enumerate(
                    ("setup-enter", "webview-build-start", "webview-build-ok", "setup-return", "first-page-load-finished"))]}}
            write("evidence/app-startup-report.json", host)
            stages = ("connection-open", "session-received", "forward-start", "native-headers", "connection-end")
            states = ("idle", "received", "forward-pending", "headers-returned", "headers-returned")
            rows = [{"type": "header", "schema": 1, "variant": "tauri-driver-2.1.0-stage-1"}]
            rows += [{"type": "event", "seq": i + 1, "connection": 1, "request": int(i != 0), "stage": stage,
                      "state": states[i], "error": "none", "elapsedUs": i + 1} for i, stage in enumerate(stages)]
            rows += [{"type": "summary", "lastSequence": 5, "attempted": 5, "enqueued": 5, "written": 5, "dropped": 0, "sealed": True, "pendingRequests": 0}]
            (temp / "tools/driver-stage.jsonl").write_text("\n".join(json.dumps(row, separators=(",", ":")) for row in rows) + "\n")
            def run():
                result = subprocess.run([sys.executable, "-B", str(ROOT / "scripts/wry_probe_ci.py"), "publish", "--root", str(ROOT), "--temp", str(temp)],
                    env={**os.environ, "CONTROL_HEAD": "a" * 40}, capture_output=True, timeout=15)
                self.assertEqual(result.stderr, b"")
                return result.returncode, json.loads(result.stdout.decode().splitlines()[1])
            self.assertEqual(run()[0], 0)
            for name in ("overlay.json", "evidence/native-observation.json"):
                data = (temp / name).read_bytes(); (temp / name).unlink()
                self.assertEqual(run()[0], 1)
                (temp / name).write_bytes(data)
            host["startup"]["events"] = [None]
            write("evidence/app-startup-report.json", host)
            code, result = run()
            self.assertEqual(code, 1)
            self.assertEqual(result["files"]["host"]["data"], {"state": "unavailable-or-rejected"})
            self.assertEqual(result["files"]["steps"]["data"][-1]["stage"], "final-identity")

    def test_retained_actual_failure_projection_stays_red(self):
        value = json.loads((ROOT / "scripts/tests/fixtures/wry240-baseline.json").read_text())
        native = ci.native_summary(value["native"])
        observation = ci.observation_summary(value["observation"])
        self.assertEqual((native["nativeTestsRun"], native["nativeTestsPassed"]), (1, 0))
        self.assertIs(native["normalCloseVerified"], False)
        self.assertEqual((native["cleanupFailures"], native["childHandleDisposeFailures"]), (0, 0))
        self.assertEqual((observation["phase"], observation["errorClass"], observation["elapsedMs"]),
                         ("session-create", "timeout", 15013))
        self.assertEqual(observation["sessionRequests"][-1], {"outcome": "timeout", "elapsedMs": 15008, "httpStatus": None})

    def test_exact_generated_permission_contract_and_alias_negatives(self):
        expected = json.loads((ROOT / "scripts/wry_probe_permissions.json").read_text())
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            folder = root / "ui/tauri/src-tauri/permissions"
            folder.mkdir(parents=True)
            for name in ("shell.toml", "resident-source.toml"): (folder / name).write_bytes(b"tracked")
            ci.permissions(root, False)
            with self.assertRaises(ValueError): ci.permissions(root, True)
            generated = folder / "autogenerated"; generated.mkdir()
            for name, data in expected.items(): (generated / name).write_bytes(data.encode())
            with self.assertRaises(ValueError): ci.permissions(root, False)
            ci.permissions(root, True)
            bad = generated / "unknown.json"; bad.write_bytes(b"{}")
            with self.assertRaises(ValueError): ci.permissions(root, True)
            bad.unlink()
            name, data = next(iter(expected.items()))
            path = generated / name; path.write_bytes(data.encode() + b"\n")
            with self.assertRaises(ValueError): ci.permissions(root, True)
            path.unlink(); outside = root / "outside"; outside.write_bytes(data.encode()); path.symlink_to(outside)
            with self.assertRaises(ValueError): ci.permissions(root, True)

    def test_public_projection_never_copies_arbitrary_strings_or_keys(self):
        private = "PRIVATE_SENTINEL"
        result = ci.native_summary({"exception": private, "nativeTestsRun": private,
                                    "startupFailureFacts": {"driverIdentity": private, "pid": private}})
        observation = ci.observation_summary({"status": private, "phase": private, "errorCode": private,
                                              "requests": [{"operation": "create-session", "outcome": private,
                                                            "elapsedMs": private, "payload": private}]})
        self.assertNotIn(private, json.dumps([result, observation]))
        for value in ([{"stage": private, "exitCode": 0}], [{"stage": "pins", "exitCode": True}],
                      [{"stage": "pins", "exitCode": 0}] * 2):
            with self.assertRaises(ValueError): ci.safe_steps(value)
        with self.assertRaises(ValueError): ci.safe_meta({"sourceCommit": private})

    def test_actual_publisher_subprocess_keeps_partial_failure_safe(self):
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory)
            (temp / "steps.json").write_text('[{"stage":"pins","exitCode":1}]')
            (temp / "launch.json").write_text('{"private":"PRIVATE_SENTINEL"}')
            result = subprocess.run([sys.executable, "-B", str(ROOT / "scripts/wry_probe_ci.py"),
                                     "publish", "--root", str(ROOT), "--temp", str(temp)],
                                    env={**os.environ, "CONTROL_HEAD": "a" * 40}, capture_output=True, timeout=15)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stderr, b"")
            self.assertNotIn(b"PRIVATE_SENTINEL", result.stdout)
            lines = result.stdout.decode().splitlines()
            self.assertEqual(lines[0], "WRY_DIAGNOSTIC_RECORD_BEGIN")
            body = json.loads(lines[1])
            self.assertFalse(body["allOriginalAndDiagnosticChecksPassed"])
            self.assertEqual(body["files"]["steps"]["data"], [{"stage": "pins", "exitCode": 1}])
            self.assertEqual(body["files"]["launch"]["data"], {"state": "unavailable-or-rejected"})

    def test_complete_partial_and_callback_before_api(self):
        for stages in ((), record.STAGES[:1], record.STAGES[:3], record.STAGES,
                       (record.STAGES[0], record.STAGES[2], record.STAGES[1], *record.STAGES[3:]),
                       (record.STAGES[0], record.STAGES[1], record.STAGES[3],
                        record.STAGES[4], record.STAGES[2])):
            self.assertEqual(record.validate(example(stages)), example(stages))

    def test_unknown_missing_duplicate_count_loss_time_and_thread_are_rejected(self):
        bad = []
        for key, value in (("registered", False), ("sealed", False), ("dropped", 1),
                           ("inFlight", 1), ("sealElapsedUs", 0), ("attempted", 9),
                           ("recorded", True), ("schema", True), ("secret", "not-published")):
            row = example(); row[key] = value; bad.append(row)
        row = example(); del row["purpose"]; bad.append(row)
        for key, value in (("stage", "unknown"), ("stage", []), ("seq", True),
                           ("elapsedUs", -1), ("onTraceThread", False), ("extra", "hidden")):
            row = example(); row["events"][0][key] = value; bad.append(row)
        for stages in (("controller-enter",), ("environment-enter", "environment-wait-returned"),
                       ("environment-enter", "environment-enter")):
            bad.append(example(stages))
        for value in bad:
            with self.subTest(value=value):
                with self.assertRaises((ValueError, TypeError)):
                    record.validate(value)

    def test_actual_file_boundary_rejects_unknown_closed_symlink_and_noncanonical(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "probe.json"
            text = json.dumps(example(), separators=(",", ":"))
            path.write_text(text)
            self.assertEqual(record.read_record(path, {"appStartupFileClosed": True}), example())
            for collection in ({}, {"appStartupFileClosed": False}, None):
                with self.assertRaises(ValueError): record.read_record(path, collection)
            for bad in (text + "\n", text.replace('"schema":1', '"schema":1,"schema":1'), "x" * 2049):
                path.write_text(bad)
                with self.assertRaises(ValueError): record.read_record(path, {"appStartupFileClosed": True})
            path.write_text(text)
            link = Path(directory) / "alias.json"; link.symlink_to(path)
            with self.assertRaises(ValueError): record.read_record(link, {"appStartupFileClosed": True})


class OverlayTests(unittest.TestCase):
    def test_actual_cli_invalid_arguments_never_echo_values_or_paths(self):
        for args in ([], ["--unknown", "PRIVATE_SENTINEL"], ["--archive", "PRIVATE_SENTINEL"]):
            result = subprocess.run([sys.executable, "-B", str(ROOT / "scripts/prepare_wry_probe.py"), *args],
                                    capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stderr, b"")
            self.assertEqual(result.stdout, b'{"code":"WRY_PROBE_PREPARATION_REJECTED"}\n')

    def test_real_cross_crate_hook_and_rust_record_reach_strict_reader(self):
        # Real link/record/file seam, not a model of COM or the Windows timeout.
        rustc = os.environ.get("WRY_PROBE_RUSTC")
        if not rustc: self.fail("WRY_PROBE_RUSTC must name an already available compiler")
        with tempfile.TemporaryDirectory() as directory:
            tmp = Path(directory)
            client = tmp / "client.rs"
            client.write_text('unsafe extern "C" { fn rescue_host_wry_stage(code: u8); }\n'
                              'pub fn emit() { for code in 1..=8 { unsafe { rescue_host_wry_stage(code); } } }\n')
            lib = tmp / "libprobe_client.rlib"
            subprocess.run([rustc, "--edition", "2024", "--crate-name", "probe_client", "--crate-type", "rlib",
                            str(client), "-o", str(lib)], check=True, capture_output=True, timeout=30)
            source = ROOT / "ui/tauri/src-tauri/src/wry_startup_probe.rs"
            main = tmp / "main.rs"
            main.write_text('#[path = ' + json.dumps(str(source)) + '] mod probe;\n'
                            'fn main() { let p = probe::Probe::memory(); assert!(p.register());\n'
                            'probe_client::emit(); print!("{}", p.snapshot(true, true)); }\n')
            exe = tmp / "link-fixture.exe"
            subprocess.run([rustc, "--edition", "2024", str(main), "--extern", f"probe_client={lib}",
                            "-o", str(exe)], check=True, capture_output=True, timeout=30)
            result = subprocess.run([str(exe)], check=True, capture_output=True, timeout=10)
            path = tmp / "result.json"; path.write_bytes(result.stdout)
            value = record.read_record(path, {"appStartupFileClosed": True})
            self.assertEqual(tuple(row["stage"] for row in value["events"]), record.STAGES)
            self.assertTrue(all(row["onTraceThread"] for row in value["events"]))

    def test_actual_lock_projection_accepts_only_two_exact_representations(self):
        # HEAD's own blob exists in a depth-one checkout, even after prepare has
        # intentionally replaced the working lock for the diagnostic path patch.
        data = ci.git(ROOT, "show", "HEAD:ui/tauri/src-tauri/Cargo.lock")
        lf = prepare.project_lock(data)
        self.assertEqual(prepare.project_lock(data.replace(b"\n", b"\r\n")), lf)
        self.assertEqual(lf.count(b'name = "wry"\nversion = "0.57.1"\n'), 1)
        self.assertNotIn(prepare.CRATE_SHA.encode(), lf)
        for bad in (data + b"\n", data.replace(b"\n", b"\r", 1),
                    data.replace(b"\n", b"\r\n", 1), lf):
            with self.assertRaises(ValueError): prepare.project_lock(bad)

    def test_pinned_archive_actual_projection(self):
        # Explicit fixture archive set by the runner after fixed checksum download.
        path = os.environ.get("WRY_PROBE_CRATE")
        if not path: self.fail("WRY_PROBE_CRATE must name the exact archive; no skipped projection")
        archive = Path(path)
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "vendor"
            meta = prepare.materialize(archive, out)
            self.assertEqual(meta["originalSha256"], "9ef674af12e52bc3519b0e300752c1042798b7f336e0e0dafca6a8944f348888")
            self.assertEqual(meta["patchedSha256"], "8d4a637727ce94dab6c6fb8e676dde8f2e7a4d0c10681fef0b82dbda42654700")
            text = (out / prepare.FILE).read_text()
            self.assertEqual(text.count("rescue_host_wry_stage("), 9)
            self.assertEqual(text.count("let sent = tx.send(result)"), 2)
            self.assertEqual(text.count("let result = webview2_com::wait_with_pump(rx)?;"), 2)
            with self.assertRaises(ValueError): prepare.materialize(archive, out)
            wrong = Path(directory) / "wrong.crate"; wrong.write_bytes(archive.read_bytes() + b"x")
            with self.assertRaises(ValueError): prepare.materialize(wrong, Path(directory) / "wrong")
            with self.assertRaises(ValueError): prepare.patch(text)


if __name__ == "__main__": unittest.main()
