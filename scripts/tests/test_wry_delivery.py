"""Actual overlay/protocol/recorder seam with SIMULATED SDK and message delivery."""
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import prepare_wry_probe as old
import prepare_wry_delivery as prepare
import wry_delivery_record as record
import wry_probe_ci as ci


def callback(text, function, arg):
    section = text[text.index(f"  fn {function}("):]
    start = section.index(f"move |error_code, {arg}| {{")
    brace = section.index("{", start)
    depth = 0
    for end in range(brace, len(section)):
        depth += (section[end] == "{") - (section[end] == "}")
        if depth == 0: return section[start:end + 1]
    raise AssertionError("CALLBACK_SOURCE_REJECTED")


class DeliveryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        for name in ("WRY_PROBE_CRATE", "WRY_COMPLETION_CRATE", "WRY_PROBE_RUSTC"):
            if not os.environ.get(name): raise AssertionError(name + " prerequisite required; no skip")
        cls.archive = Path(os.environ["WRY_PROBE_CRATE"])
        cls.com_archive = Path(os.environ["WRY_COMPLETION_CRATE"])
        cls.rustc = os.environ["WRY_PROBE_RUSTC"]
        cls.owner = tempfile.TemporaryDirectory(prefix="delivery-seam-")
        cls.temp = Path(cls.owner.name)
        cls.wry, cls.com = cls.temp / "wry", cls.temp / "completion"
        cls.meta = prepare.materialize(cls.archive, cls.wry, cls.com_archive, cls.com)
        (cls.temp / "protocol.rs").write_bytes((cls.com / "src/rescue_host_delivery.rs").read_bytes())
        com = (cls.com / "src/lib.rs").read_bytes()
        (cls.temp / "pump.rs").write_bytes(com[com.index(b"pub fn wait_with_pump<T>"):])
        (cls.temp / "runtime.rs").write_bytes((ROOT / "scripts/tests/fixtures/wry_delivery_runtime.rs").read_bytes())
        cls.lib = cls.temp / "libwebview2_com.rlib"
        cls.compile(["--crate-type=rlib", "--crate-name=webview2_com", str(cls.temp / "runtime.rs"), "-o", str(cls.lib)])
        wry = (cls.wry / old.FILE).read_text()
        cls.callbacks = (callback(wry, "create_environment", "environment"), callback(wry, "create_controller", "controller"))
        cls.executables = {}
        for mutation in ("none", "duplicate-hook", "missing-tail"):
            callbacks = list(cls.callbacks)
            if mutation == "duplicate-hook":
                for i, text in enumerate(callbacks):
                    line = "webview2_com::rescue_host_delivery::Completion::sent(__delivery_token, sent.is_ok());"
                    assert text.count(line) == 1
                    callbacks[i] = text.replace(line, line + line, 1)
            if mutation == "missing-tail":
                for i, text in enumerate(callbacks):
                    indent = "            " if i == 0 else "        "
                    line = indent + "sent\n"
                    assert text.count(line) == 1
                    callbacks[i] = text.replace(line, indent + "std::mem::forget(_delivery_completion);\n" + line, 1)
            template = (ROOT / "scripts/tests/fixtures/wry_delivery_main.rs").read_text()
            template = template.replace('"@RECORDER@"', json.dumps((ROOT / "ui/tauri/src-tauri/src/wry_delivery_probe.rs").as_posix()))
            template = template.replace("@ENV_CALLBACK@", callbacks[0]).replace("@CONTROLLER_CALLBACK@", callbacks[1])
            main = cls.temp / (mutation + ".rs"); main.write_text(template)
            exe = cls.temp / (mutation + ".exe")
            cls.compile([str(main), "--extern", f"webview2_com={cls.lib}", "-o", str(exe)])
            cls.executables[mutation] = exe

    @classmethod
    def compile(cls, args):
        result = subprocess.run([cls.rustc, "--edition=2024", *args], capture_output=True, timeout=30)
        if result.returncode: raise AssertionError("DELIVERY_FIXTURE_COMPILE_REJECTED")

    @classmethod
    def tearDownClass(cls): cls.owner.cleanup()

    def observe(self, case, mutation="none"):
        result = subprocess.run([str(self.executables[mutation]), case], capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, "DELIVERY_FIXTURE_CASE_REJECTED:" + case)
        path = self.temp / "record.json"; path.write_bytes(result.stdout)
        return record.read_record(path, {"appStartupFileClosed": True})

    @staticmethod
    def synthetic_queries(value):
        # Explicit synthetic Windows-query policy input, never relabels runtime data.
        value = copy.deepcopy(value)
        for row in value["scopes"]: row["apartmentAtScopeEntry"] = "sta"
        for row in value["samples"]:
            if row["state"] == "observed": row["sentContext"] = "not-sent-context"
        return value

    def test_actual_api_get_dispatch_and_outside_binding(self):
        for case, phase, relation in (("api", "sdk-api", "same-creation"), ("get", "get-message", "original-wait"),
                                      ("dispatch", "dispatch-message", "original-wait"), ("outside", "outside", "same-creation")):
            value = self.observe(case)
            self.assertTrue(all(row["phase"] == phase and row["relation"] == relation for row in value["samples"]))
            self.assertTrue(record.complete(self.synthetic_queries(value)))

    def test_actual_nested_wrong_token_other_thread_and_unwrapped_wait_are_unknown(self):
        for case in ("nested", "nested-scope", "wrong-token", "other-thread", "unwrapped-wait"):
            value = self.observe(case)
            self.assertFalse(record.complete(self.synthetic_queries(value)))
            self.assertTrue(all(row["phase"] == "unknown" for row in value["samples"]))

    def test_actual_callback_send_and_pump_error_values_are_preserved(self):
        self.assertTrue(record.complete(self.synthetic_queries(self.observe("callback-error"))))
        for case in ("disconnected", "pump-error", "late"):
            value = self.observe(case)
            self.assertFalse(record.complete(self.synthetic_queries(value)))
        late = self.observe("late")
        self.assertTrue(all(row["phase"] == "unknown" for row in late["samples"]))

    def test_actual_duplicate_and_missing_tail_mutations_are_detected(self):
        for mutation in ("duplicate-hook", "missing-tail"):
            value = self.observe(mutation, mutation)
            self.assertFalse(record.complete(self.synthetic_queries(value)))
            self.assertTrue(any(row["state"] in {"missing", "ambiguous"} for row in value["samples"]))

    def test_both_inverse_projections_restore_exact_upstream_bytes(self):
        wry = (self.wry / old.FILE).read_text()
        com = (self.com / "src/lib.rs").read_text()
        f = prepare.patch_wry(wry, True)
        self.assertEqual(hashlib.sha256(f.encode()).hexdigest(), "8d4a637727ce94dab6c6fb8e676dde8f2e7a4d0c10681fef0b82dbda42654700")
        self.assertEqual(hashlib.sha256(prepare.patch_com(com, True).encode()).hexdigest(), prepare.COM_ORIGINAL)
        self.assertEqual(wry.count("let sent = tx.send(result)"), 2)
        self.assertEqual(wry.count("let result = __delivery.wait(rx)?;"), 2)
        self.assertIn("match WindowsAndMessaging::GetMessageA(&mut msg, None, 0, 0).0 {\n                -1 =>", com)

    def test_exact_lock_lf_crlf_and_wrong_or_already_projected_inputs(self):
        blob = ci.git(ROOT, "show", "HEAD:ui/tauri/src-tauri/Cargo.lock")
        projected = prepare.project_lock(blob)
        self.assertEqual(prepare.project_lock(blob.replace(b"\n", b"\r\n")), projected)
        self.assertNotIn(prepare.COM_SHA.encode(), projected)
        self.assertNotIn(old.CRATE_SHA.encode(), projected)
        for bad in (blob + b"x", projected, blob.replace(b"\n", b"\r", 1), blob.replace(b"\n", b"\r\n", 1)):
            with self.assertRaises(ValueError): prepare.project_lock(bad)

    def test_closed_shapes_and_unknown_partial_never_authorize(self):
        value = self.synthetic_queries(self.observe("get"))
        for key, replacement in (("registered", False), ("sealed", False), ("lossDetected", True),
                                 ("inFlight", 1), ("scopeAttemptsCapped", 3)):
            bad = copy.deepcopy(value); bad[key] = replacement
            self.assertFalse(record.complete(bad))
        for field, replacement in (("phase", "unknown"), ("relation", "unknown"), ("sentContext", "unknown"), ("onScopeThread", False)):
            bad = copy.deepcopy(value); bad["samples"][0][field] = replacement
            self.assertFalse(record.complete(bad))
        for field, replacement in (("phase", "PRIVATE_SENTINEL"), ("phase", []), ("elapsedUs", True), ("seq", 99)):
            bad = copy.deepcopy(value); bad["samples"][0][field] = replacement
            with self.assertRaises(ValueError): record.validate(bad)
        for missing in ("registered", "samples"):
            bad = copy.deepcopy(value); del bad[missing]
            with self.assertRaises(ValueError): record.validate(bad)
        bad = copy.deepcopy(value); bad["secret"] = "PRIVATE_SENTINEL"
        with self.assertRaises(ValueError): record.validate(bad)
        bad = copy.deepcopy(value); bad["samples"].reverse()
        with self.assertRaises(ValueError): record.validate(bad)
        bad = copy.deepcopy(value); bad["samples"][1]["seq"] = 1
        self.assertFalse(record.complete(bad))
        bad = copy.deepcopy(value); bad["samples"][1]["elapsedUs"] = 0
        self.assertFalse(record.complete(bad))

    def test_actual_file_boundary_rejects_missing_close_duplicate_truncation_and_alias(self):
        value = self.observe("api"); path = self.temp / "boundary.json"
        data = json.dumps(value, separators=(",", ":")).encode(); path.write_bytes(data)
        with self.assertRaises(ValueError): record.read_record(path, {"appStartupFileClosed": False})
        for bad in (data[:-1], data + b"\n", b'{"schema":1,' + data[1:], data + b" " * 8192):
            path.write_bytes(bad)
            with self.assertRaises(ValueError): record.read_record(path, {"appStartupFileClosed": True})
        path.write_bytes(data); alias = self.temp / "alias.json"
        if hasattr(os, "link"):
            os.link(path, alias)
            try:
                with self.assertRaises(ValueError): record.read_record(path, {"appStartupFileClosed": True})
            finally: alias.unlink()

    def test_preparer_wrong_archive_existing_output_and_non_secret_cli(self):
        with tempfile.TemporaryDirectory() as directory:
            tmp = Path(directory); wrong = tmp / "bad.crate"; wrong.write_bytes(self.com_archive.read_bytes() + b"x")
            with self.assertRaises(ValueError): prepare.materialize(self.archive, tmp / "wry", wrong, tmp / "completion")
            self.assertFalse((tmp / "wry").exists())
        for args in ([], ["--unknown", "PRIVATE_SENTINEL"]):
            result = subprocess.run([sys.executable, "-B", str(ROOT / "scripts/prepare_wry_delivery.py"), *args], capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 1); self.assertEqual(result.stderr, b"")
            self.assertIn(result.stdout, (b'{"code":"WRY_DELIVERY_PREPARATION_REJECTED"}\n', b'{"code":"WRY_DELIVERY_PREPARATION_REJECTED"}\r\n'))

    def project(self, directory, crlf=False):
        root, tools, helper = directory / "source", directory / "runtime", directory / "helper"
        permission = root / "ui/tauri/src-tauri/permissions"
        permission.mkdir(parents=True); tools.mkdir()
        (root / "immutable.txt").write_bytes(b"fixed\n")
        blob = ci.git(ROOT, "show", "HEAD:ui/tauri/src-tauri/Cargo.lock")
        (permission.parent / "Cargo.lock").write_bytes(blob)
        for name in ("shell.toml", "resident-source.toml"):
            (permission / name).write_bytes((ROOT / "ui/tauri/src-tauri/permissions" / name).read_bytes())
        subprocess.run(["git", "init", "-q", str(root)], check=True, capture_output=True, timeout=10)
        ci.git(root, "-c", "core.autocrlf=false", "add", ".")
        ci.git(root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
        self.assertEqual(ci.git(root, "rev-list", "--count", "HEAD").strip(), b"1")
        absent = subprocess.run(["git", "-C", str(root), "cat-file", "-e", "ea2fda12231044ad6d3907ac4db1a10cf02fb2e6"], capture_output=True, timeout=10)
        self.assertNotEqual(absent.returncode, 0)
        for relative in ("scripts/prepare_wry_probe.py", "scripts/prepare_wry_delivery.py", "tools/wry-delivery/protocol.rs"):
            path = helper / relative; path.parent.mkdir(parents=True, exist_ok=True)
            data = (ROOT / relative).read_bytes()
            path.write_bytes(data.replace(b"\n", b"\r\n") if crlf else data)
        shutil.copyfile(self.archive, tools / "wry.crate")
        shutil.copyfile(self.com_archive, tools / "completion.crate")
        command = [sys.executable, "-B", str(helper / "scripts/prepare_wry_delivery.py"), "--archive", str(tools / "wry.crate"),
                   "--output", str(tools / "wry"), "--completion-archive", str(tools / "completion.crate"),
                   "--completion-output", str(tools / "completion"), "--project", str(root)]
        result = subprocess.run(command, capture_output=True, timeout=20)
        self.assertEqual(result.returncode, 0, "MATERIALIZED_PREPARE_REJECTED")
        meta = ci.safe_meta(json.loads(result.stdout), True, True)
        expected = json.loads((ROOT / "scripts/wry_probe_permissions.json").read_text())
        generated = permission / "autogenerated"; generated.mkdir()
        for name, data in expected.items(): (generated / name).write_bytes(data.encode())
        return root, tools, meta

    def test_actual_materialized_cli_lf_crlf_and_identity_boundaries(self):
        for crlf in (False, True):
            with tempfile.TemporaryDirectory() as directory:
                root, tools, meta = self.project(Path(directory), crlf)
                identity = ci.source_identity(root, tools / "wry", tools / "wry.crate", True)
                self.assertTrue(identity["patchConfigVerified"])
                boundary = [sys.executable, "-B", str(ROOT / "scripts/wry_probe_ci.py"), "permissions",
                            "--root", str(root), "--temp", str(tools), "--stage", "build"]
                env = dict(os.environ, WRY_DELIVERY_ENABLED="1")
                self.assertEqual(subprocess.run(boundary, env=env, capture_output=True, timeout=10).returncode, 0)
                generated = root / "ui/tauri/src-tauri/permissions/autogenerated/get_shell_status.toml"
                content = generated.read_bytes(); generated.unlink()
                self.assertEqual(subprocess.run(boundary, env=env, capture_output=True, timeout=10).returncode, 1)
                generated.write_bytes(content + b"x")
                self.assertEqual(subprocess.run(boundary, env=env, capture_output=True, timeout=10).returncode, 1)
                generated.write_bytes(content)
                self.assertEqual(meta["overlayLockSha256"], identity["overlayLockSha256"])
                protocol = tools / "completion/src/rescue_host_delivery.rs"
                original = protocol.read_bytes()
                for bad in (original + b"x", original.replace(b"\n", b"\r\n", 1)):
                    protocol.write_bytes(bad)
                    with self.assertRaises(ValueError): ci.source_identity(root, tools / "wry", tools / "wry.crate", True)
                protocol.write_bytes(original)
                extra = tools / "completion/unknown.rs"; extra.write_bytes(b"unknown")
                with self.assertRaises(ValueError): ci.source_identity(root, tools / "wry", tools / "wry.crate", True)
                extra.unlink()
                config = tools / "wry-probe.toml"; config_data = config.read_bytes(); config.write_bytes(config_data + b"\n")
                with self.assertRaises(ValueError): ci.source_identity(root, tools / "wry", tools / "wry.crate", True)
                config.write_bytes(config_data)
                marker = root / "immutable.txt"; marker.write_bytes(b"changed")
                with self.assertRaises(subprocess.SubprocessError): ci.source_identity(root, tools / "wry", tools / "wry.crate", True)
                marker.write_bytes(b"fixed\n")
                # Synthetic Windows reparse metadata exercises the actual lstat guard.
                original_lstat = Path.lstat
                def reparse(path, *args, **kwargs):
                    info = original_lstat(path, *args, **kwargs)
                    if path == protocol:
                        return SimpleNamespace(st_mode=info.st_mode, st_nlink=info.st_nlink, st_file_attributes=1024)
                    return info
                with patch.object(Path, "lstat", reparse):
                    with self.assertRaises(ValueError): ci.source_identity(root, tools / "wry", tools / "wry.crate", True)
                if os.name != "nt":
                    backup = tools / "protocol-backup"; backup.write_bytes(original)
                    protocol.unlink(); protocol.symlink_to(backup)
                    with self.assertRaises(ValueError): ci.source_identity(root, tools / "wry", tools / "wry.crate", True)
                    protocol.unlink(); protocol.write_bytes(original)

    def test_actual_identity_cli_rejects_missing_wrong_pins_and_keeps_previous_record(self):
        with tempfile.TemporaryDirectory() as directory:
            root, tools, meta = self.project(Path(directory))
            for folder in ("fixture", "unpacked", "tools/tauri/bin", "tools/edge", "tools/tauri-driver-2.1.0"):
                (tools / folder).mkdir(parents=True, exist_ok=True)
            for folder in ("fixture", "unpacked"):
                (tools / folder / "rescue-host-shell.exe").write_bytes(b"dummy-not-an-executable")
                (tools / folder / "BUILD.json").write_bytes(b"{}")
            for name in ("fixture.zip", "tools/tauri/bin/tauri-driver.exe", "tools/edge/msedgedriver.exe", "tools/tauri-driver-2.1.0/Cargo.lock"):
                (tools / name).write_bytes(b"dummy-unit-input")
            patch_path = root / "tools/native-driver-trace/driver.patch"
            patch_path.parent.mkdir(parents=True); patch_path.write_bytes(b"fixed unit input")
            # Include this immutable input before the expected head/tree is selected.
            ci.git(root, "add", "tools/native-driver-trace/driver.patch")
            ci.git(root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "input")
            env = dict(os.environ, WRY_DELIVERY_ENABLED="1", WRY_PIN=ci.git(root, "rev-parse", "HEAD").decode().strip(),
                       WRY_TREE=ci.git(root, "rev-parse", "HEAD^{tree}").decode().strip())
            command = [sys.executable, "-B", str(ROOT / "scripts/wry_probe_ci.py"), "identity", "--root", str(root), "--temp", str(tools), "--stage", "fixture"]
            result = subprocess.run(command, env=env, capture_output=True, timeout=20)
            self.assertEqual(result.returncode, 0, "IDENTITY_CLI_REJECTED")
            before = (tools / "launch.json").read_bytes()
            for key, bad in (("WRY_PIN", "0" * 40), ("WRY_TREE", "1" * 40), ("WRY_PIN", None)):
                wrong = dict(env)
                if bad is None: wrong.pop(key)
                else: wrong[key] = bad
                red = subprocess.run(command, env=wrong, capture_output=True, timeout=20)
                self.assertEqual(red.returncode, 1)
                self.assertEqual((tools / "launch.json").read_bytes(), before)
                self.assertIn(red.stdout, (b'{"code":"WRY_DIAGNOSTIC_RECORD_REJECTED"}\n', b'{"code":"WRY_DIAGNOSTIC_RECORD_REJECTED"}\r\n'))

    def test_actual_publisher_preserves_native_verdict_and_partial_phase_evidence(self):
        # Synthetic complete wrapper world; actual phase bytes come from the compiled seam.
        # This tests publisher policy, never native ownership/cleanup or SDK success.
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory)
            for folder in ("evidence", "tools/app-startup", "unpacked"):
                (temp / folder).mkdir(parents=True, exist_ok=True)
            def write(name, value): (temp / name).write_bytes(ci.canonical(value))
            write("steps.json", [{"stage": stage, "exitCode": 0} for stage in ci.STEPS])
            native = dict(nativeTestsRun=1, nativeTestsPassed=1, normalCloseVerified=True, cleanupStarted=True,
                          cleanupFailures=0, childHandleDisposeFailures=0, driverExitObservedAfterCleanup=True, appStartupFileClosed=True)
            write("evidence/native-result.json", native)
            write("evidence/native-observation.json", dict(status="webview-passed-awaiting-close", phase="complete", elapsedMs=100,
                documentObservations=1, droppedRequests=0, errorCode="", requests=[
                    dict(operation="create-session", outcome="started", elapsedMs=0),
                    dict(operation="create-session", outcome="ok", elapsedMs=80, status=200)]))
            from wry_probe_record import STAGES
            write("tools/app-startup/wry-completion-probe.json", dict(schema=1, variant="wry-completion-probe-1", purpose="diagnostic-hints-only",
                registered=True, sealed=True, sealElapsedUs=20, attempted=8, recorded=8, dropped=0, inFlight=0,
                events=[dict(seq=i+1, stage=s, elapsedUs=i+1, onTraceThread=True) for i, s in enumerate(STAGES)]))
            phase = self.synthetic_queries(self.observe("get")); write("tools/app-startup/wry-sdk-delivery.json", phase)
            identity = {k: "a" * 64 for k in ("overlayLockSha256", "vendorTreeSha256", "exeSha256", "driverSha256", "archiveSha256",
                "edgeDriverSha256", "driverPatchSha256", "driverLockSha256", "completionVendorTreeSha256")}
            identity.update(sourceCommit=ci.git(ROOT, "rev-parse", "HEAD").decode().strip(),
                            sourceTree=ci.git(ROOT, "rev-parse", "HEAD^{tree}").decode().strip(), patchConfigVerified=True)
            write("launch.json", identity); write("final.json", identity)
            meta = dict(self.meta, originalLockSha256=old.LOCK_SHA, overlayLockSha256="a" * 64); write("overlay.json", meta)
            write("unpacked/BUILD.json", dict(schema=1, variant="host-startup-diagnostic-1", sourceCommit=identity["sourceCommit"],
                features=["custom-protocol", "startup-diagnostics"], profile="release", exeSha256="a" * 64, distribution=False))
            names = ("setup-enter", "webview-build-start", "webview-build-ok", "setup-return", "first-page-load-finished")
            write("evidence/app-startup-report.json", {"diagnostic":"complete", "startup":{"phase":"first-page-load-finished",
                "events":[dict(seq=i+1, stage=s, elapsedUs=i+1) for i,s in enumerate(names)]}})
            stages = ("connection-open", "session-received", "forward-start", "native-headers", "connection-end")
            states = ("idle", "received", "forward-pending", "headers-returned", "headers-returned")
            driver = [dict(type="header", schema=1, variant="tauri-driver-2.1.0-stage-1")]
            driver += [dict(type="event", seq=i+1, connection=1, request=int(i!=0), stage=s, state=states[i], error="none", elapsedUs=i+1) for i,s in enumerate(stages)]
            driver += [dict(type="summary", lastSequence=5, attempted=5, enqueued=5, written=5, dropped=0, sealed=True, pendingRequests=0)]
            (temp / "tools/driver-stage.jsonl").write_text("\n".join(json.dumps(r,separators=(",", ":")) for r in driver)+"\n")
            def publish():
                r = subprocess.run([sys.executable, "-B", str(ROOT / "scripts/wry_probe_ci.py"), "publish", "--root", str(ROOT), "--temp", str(temp)],
                    env={**os.environ,"CONTROL_HEAD":"a"*40,"WRY_DELIVERY_ENABLED":"1"}, capture_output=True, timeout=15)
                self.assertEqual(r.stderr,b""); self.assertNotIn(b"PRIVATE_SENTINEL",r.stdout)
                lines=r.stdout.decode().splitlines(); self.assertEqual((lines[0],lines[-1]),("WRY_DIAGNOSTIC_RECORD_BEGIN","WRY_DIAGNOSTIC_RECORD_END"))
                body=json.loads(lines[1]);self.assertEqual(hashlib.sha256(lines[1].encode()).hexdigest(),lines[2].split("=")[1])
                for section in body["files"].values(): self.assertEqual(ci.digest(ci.canonical(section["data"])),section["sha256"])
                return r.returncode,body
            code,body=publish();self.assertEqual(code,0);self.assertTrue(body["deliveryClassificationComplete"])
            self.assertEqual(len(body["files"]),11)
            bad=copy.deepcopy(phase);bad["samples"][0]["phase"]="unknown";write("tools/app-startup/wry-sdk-delivery.json",bad)
            code,body=publish();self.assertEqual(code,1);self.assertFalse(body["deliveryClassificationComplete"])
            self.assertEqual(body["files"]["native"]["data"]["nativeTestsPassed"],1)
            bad["private"]="PRIVATE_SENTINEL";write("tools/app-startup/wry-sdk-delivery.json",bad)
            code,body=publish();self.assertEqual(code,1);self.assertEqual(body["files"]["delivery"]["data"],{"state":"unavailable-or-rejected"})
            (temp/"tools/app-startup/wry-sdk-delivery.json").unlink()
            self.assertEqual(publish()[0],1)
            write("tools/app-startup/wry-sdk-delivery.json",phase)
            native.update(nativeTestsPassed=0,normalCloseVerified=False);write("evidence/native-result.json",native)
            code,body=publish();self.assertEqual(code,1);self.assertTrue(body["deliveryClassificationComplete"])
            self.assertFalse(body["rootCauseEstablished"]);self.assertFalse(body["timeoutFixed"])


if __name__ == "__main__": unittest.main()
