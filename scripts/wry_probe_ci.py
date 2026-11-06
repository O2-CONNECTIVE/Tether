"""Single-run safe summary. No raw subprocess output is copied to the record."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

from prepare_wry_probe import Parser, project_lock, CRATE_SHA, LOCK_SHA
from verify_driver_trace import inspect as inspect_driver
from wry_probe_record import read_record

STEPS = ("pins", "rust", "prepare", "python", "node", "parse", "harness", "driver", "frontend",
         "rust-tests", "build", "fixture", "native", "host-report", "driver-report", "final-identity")


def number(value):
    return value if type(value) is int and 0 <= value <= 2**53 - 1 else None


def boolean(value):
    return value if type(value) is bool else None


def enum(value, allowed):
    return value if type(value) is str and value in allowed else "unknown"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def checkpoint(path, value):
    import tempfile
    name = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as stream:
            name = stream.name
            stream.write(canonical(value))
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
        name = None
    finally:
        if name is not None: Path(name).unlink(missing_ok=True)


def load(path, limit=65536):
    info = path.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
            or getattr(info, "st_file_attributes", 0) & 1024 or info.st_size > limit):
        raise ValueError("RECORD_UNAVAILABLE")
    data = path.read_bytes()
    if len(data) > limit: raise ValueError("RECORD_UNAVAILABLE")
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result: raise ValueError("RECORD_UNAVAILABLE")
            result[key] = value
        return result
    return json.loads(data.decode("utf-8-sig"), object_pairs_hook=unique)


def safe_steps(value):
    if not isinstance(value, list) or len(value) > len(STEPS): raise ValueError()
    last = -1
    for row in value:
        if (not isinstance(row, dict) or set(row) != {"stage", "exitCode"}
                or row["stage"] not in STEPS or STEPS.index(row["stage"]) <= last
                or type(row["exitCode"]) is not int or not 0 <= row["exitCode"] <= 65535): raise ValueError()
        last = STEPS.index(row["stage"])
    return value


def safe_meta(value, overlay=False, delivery=False):
    keys = ({"schema", "variant", "crateSha256", "originalSha256", "patchedSha256", "files",
             "originalLockSha256", "overlayLockSha256"} if overlay else
            {"sourceCommit", "sourceTree", "overlayLockSha256", "vendorTreeSha256", "exeSha256", "driverSha256", "archiveSha256",
             "edgeDriverSha256", "driverPatchSha256", "driverLockSha256"})
    if delivery:
        keys |= ({"completionCrateSha256", "completionOriginalSha256", "completionPatchedSha256",
                  "completionFiles", "protocolSha256"} if overlay else
                 {"completionVendorTreeSha256", "patchConfigVerified"})
    if not isinstance(value, dict) or set(value) != keys: raise ValueError()
    for key in keys - {"schema", "variant", "files", "completionFiles", "patchConfigVerified"}:
        length = 40 if key in {"sourceCommit", "sourceTree"} else 64
        if type(value[key]) is not str or not re.fullmatch(f"[0-9a-f]{{{length}}}", value[key]): raise ValueError()
    if delivery and not overlay and value["patchConfigVerified"] is not True: raise ValueError()
    if overlay:
        expected_variant = "wry-sdk-delivery-overlay-1" if delivery else "wry-completion-overlay-1"
        expected_post = "8d4a637727ce94dab6c6fb8e676dde8f2e7a4d0c10681fef0b82dbda42654700"
        if delivery:
            import prepare_wry_delivery as extra
            expected_post = extra.WRY_POST
            if (value["completionCrateSha256"] != extra.COM_SHA
                    or value["completionOriginalSha256"] != extra.COM_ORIGINAL
                    or value["completionPatchedSha256"] != extra.COM_POST
                    or value["completionFiles"] != extra.COM_FILES or value["protocolSha256"] != extra.PROTOCOL_SHA):
                raise ValueError()
        if (type(value["schema"]) is not int or value["schema"] != 1 or value["variant"] != expected_variant
                or value["files"] != 84 or value["crateSha256"] != CRATE_SHA or value["originalLockSha256"] != LOCK_SHA
                or value["originalSha256"] != "9ef674af12e52bc3519b0e300752c1042798b7f336e0e0dafca6a8944f348888"
                or value["patchedSha256"] != expected_post): raise ValueError()
    return value


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], stderr=subprocess.DEVNULL, timeout=30)


def source_identity(root, vendor, archive, delivery=False):
    # All tracked inputs remain exact, except the strictly projected Wry lock.
    git(root, "diff", "--exit-code", "HEAD", "--", ".", ":(exclude)ui/tauri/src-tauri/Cargo.lock")
    lock = git(root, "show", "HEAD:ui/tauri/src-tauri/Cargo.lock")
    projector = project_lock
    if delivery:
        from prepare_wry_delivery import project_lock as projector
    if (root / "ui/tauri/src-tauri/Cargo.lock").read_bytes() != projector(lock):
        raise ValueError("SOURCE_CHANGED")
    # Rebuild the expected vendor tree from the pinned archive without changing the active tree.
    import tempfile
    from prepare_wry_probe import materialize
    with tempfile.TemporaryDirectory() as directory:
        expected = Path(directory) / "expected"
        pairs = [(vendor, expected)]
        if delivery:
            from prepare_wry_delivery import materialize as make_delivery, config_bytes
            completion = vendor.parent / "completion"
            expected_completion = expected.parent / "expected-completion"
            make_delivery(archive, expected, vendor.parent / "completion.crate", expected_completion)
            pairs.append((completion, expected_completion))
            config = vendor.parent / "wry-probe.toml"
            info = config.lstat()
            if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or getattr(info, "st_file_attributes", 0) & 1024
                    or config.read_bytes() != config_bytes(vendor, completion)): raise ValueError("VENDOR_CHANGED")
        else:
            materialize(archive, expected)
        tree_hashes = []
        for actual_root, expected_root in pairs:
            names = sorted(p.relative_to(expected_root).as_posix() for p in expected_root.rglob("*"))
            actual = sorted(p.relative_to(actual_root).as_posix() for p in actual_root.rglob("*"))
            info = actual_root.lstat()
            if actual != names or not stat.S_ISDIR(info.st_mode) or getattr(info, "st_file_attributes", 0) & 1024:
                raise ValueError("VENDOR_CHANGED")
            rows = []
            for name in names:
                path, wanted = actual_root / name, expected_root / name
                info = path.lstat()
                if getattr(info, "st_file_attributes", 0) & 1024: raise ValueError("VENDOR_CHANGED")
                if wanted.is_dir():
                    if not stat.S_ISDIR(info.st_mode): raise ValueError("VENDOR_CHANGED")
                else:
                    if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or path.read_bytes() != wanted.read_bytes()):
                        raise ValueError("VENDOR_CHANGED")
                    rows.append([name, digest(path.read_bytes())])
            tree_hashes.append(digest(canonical(rows)))
    head = git(root, "rev-parse", "HEAD").decode().strip()
    tree = git(root, "rev-parse", "HEAD^{tree}").decode().strip()
    if not re.fullmatch("[0-9a-f]{40}", head) or not re.fullmatch("[0-9a-f]{40}", tree):
        raise ValueError("SOURCE_CHANGED")
    value = {"sourceCommit": head, "sourceTree": tree, "overlayLockSha256": digest(projector(lock)),
             "vendorTreeSha256": tree_hashes[0]}
    if delivery:
        value.update(completionVendorTreeSha256=tree_hashes[1], patchConfigVerified=True)
    return value


def permissions(root, required):
    # Reuse PR234's checksum/source-reviewed exact four-output contract. The
    # sequential observations are not an atomic filesystem identity guarantee.
    current = root
    for name in ("ui", "tauri", "src-tauri", "permissions"):
        current = current / name
        info = current.lstat()
        if not stat.S_ISDIR(info.st_mode) or getattr(info, "st_file_attributes", 0) & 1024: raise ValueError()
    names = set()
    for path in current.iterdir():
        names.add(path.name)
        if len(names) > 3: raise ValueError()
    if names - {"autogenerated"} != {"shell.toml", "resident-source.toml"}: raise ValueError()
    if "autogenerated" not in names:
        if required: raise ValueError()
        return
    if not required: raise ValueError()
    folder = current / "autogenerated"
    info = folder.lstat()
    if not stat.S_ISDIR(info.st_mode) or getattr(info, "st_file_attributes", 0) & 1024: raise ValueError()
    expected = load(Path(__file__).with_name("wry_probe_permissions.json"), 4096)
    entries = []
    for path in folder.iterdir():
        entries.append(path)
        if len(entries) > 4: raise ValueError()
    if len(entries) != 4 or {p.name for p in entries} != set(expected): raise ValueError()
    for path in entries:
        info = path.lstat()
        if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or getattr(info, "st_file_attributes", 0) & 1024
                or info.st_size != len(expected[path.name].encode()) or path.read_bytes() != expected[path.name].encode()): raise ValueError()


def native_summary(value):
    if not isinstance(value, dict): raise ValueError("NATIVE_UNAVAILABLE")
    result = {key: number(value.get(key)) for key in
              ("nativeTestsRun", "nativeTestsPassed", "cleanupFailures", "childHandleDisposeFailures")}
    result.update({key: boolean(value.get(key)) for key in
                   ("cleanupStarted", "normalCloseVerified", "driverExitObservedAfterCleanup", "appStartupFileClosed",
                    "appCloseObservedBeforeCleanup", "closeObservedBeforeCleanup", "driverStableThroughClose")})
    # Preserve counts/liveness and uncertainty; never publish names, PIDs or exception types.
    for name in ("beforeSessionFacts", "startupFailureFacts", "startupAfterCleanupFacts", "initialLineageFacts", "afterDriverCleanupFacts"):
        facts = value.get(name)
        if isinstance(facts, dict):
            result[name] = {key: number(facts.get(key)) for key in
                            ("appCandidates", "correlatedApps", "correlatedWebviews", "omittedListeners")}
            result[name].update({key: enum(facts.get(key), {"same-identity", "absent", "reused-pid", "unknown"})
                                 for key in ("driverIdentity", "edgeIdentity")})
    return result


def observation_summary(value):
    if not isinstance(value, dict): raise ValueError("OBSERVATION_UNAVAILABLE")
    result = {"status": enum(value.get("status"), {"running", "failed", "webview-passed-awaiting-close"}),
              "phase": enum(value.get("phase"), {"bootstrap", "session-create", "origin-check", "readiness",
                  "main-screenshot", "unwired-contract", "keyboard", "diagnostics-screenshot", "result-write", "complete"}),
              "elapsedMs": number(value.get("elapsedMs")), "documentObservations": number(value.get("documentObservations")),
              "droppedRequests": number(value.get("droppedRequests")),
              "errorClass": "timeout" if value.get("errorCode") == "NATIVE_DRIVER_TIMEOUT" else
                            "none" if value.get("errorCode") == "" else "other"}
    requests = value.get("requests")
    if not isinstance(requests, list) or len(requests) > 256: raise ValueError("OBSERVATION_UNAVAILABLE")
    result["sessionRequests"] = [{"outcome": enum(row.get("outcome"),
                                {"started", "ok", "timeout", "transport", "http", "webdriver", "invalid-response"}),
                                "elapsedMs": number(row.get("elapsedMs")), "httpStatus": number(row.get("status"))}
                               for row in requests if isinstance(row, dict) and row.get("operation") == "create-session"]
    return result


def publish(root, temp, delivery=False):
    files = {}
    def add(name, operation):
        try: value = operation()
        except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError):
            value = {"state": "unavailable-or-rejected"}
        files[name] = {"sha256": digest(canonical(value)), "data": value}
    add("steps", lambda: safe_steps(load(temp / "steps.json", 4096)))
    add("native", lambda: native_summary(load(temp / "evidence/native-result.json")))
    add("observation", lambda: observation_summary(load(temp / "evidence/native-observation.json")))
    add("wry", lambda: read_record(temp / "tools/app-startup/wry-completion-probe.json",
                                   load(temp / "evidence/native-result.json")))
    if delivery:
        from wry_delivery_record import read_record as read_delivery
        add("delivery", lambda: read_delivery(temp / "tools/app-startup/wry-sdk-delivery.json",
                                             load(temp / "evidence/native-result.json")))
    def driver():
        path = temp / "tools/driver-stage.jsonl"
        if path.is_symlink() or path.stat().st_size > 8192: raise ValueError()
        return inspect_driver(path.read_text(encoding="utf-8"))
    add("driver", driver)
    # Host CLI output is validated at its existing source boundary; retain only its finite stage projection.
    def host():
        value = load(temp / "evidence/app-startup-report.json", 8192)
        stages = value["startup"]["events"]
        if not isinstance(stages, list) or len(stages) > 6 or any(not isinstance(row, dict) for row in stages): raise ValueError()
        allowed = {"setup-enter", "webview-build-start", "webview-build-ok", "webview-build-error", "setup-return", "first-page-load-finished"}
        if any(set(row) != {"seq", "stage", "elapsedUs"} or row["stage"] not in allowed
               or number(row["elapsedUs"]) is None or number(row["seq"]) is None for row in stages): raise ValueError()
        return {"diagnostic": enum(value.get("diagnostic"), {"complete", "incomplete"}),
                "phase": enum(value["startup"].get("phase"), {"first-page-load-finished", "webview-build-error",
                    "webview-built-page-load-not-observed", "webview-build-not-observed", "setup-entered-build-not-observed", "setup-not-observed", "diagnostic-incomplete"}),
                "events": [{"stage": enum(row.get("stage"), allowed), "elapsedUs": number(row.get("elapsedUs"))} for row in stages]}
    add("host", host)
    add("launch", lambda: safe_meta(load(temp / "launch.json", 4096), delivery=delivery))
    add("final", lambda: safe_meta(load(temp / "final.json", 4096), delivery=delivery))
    add("overlay", lambda: safe_meta(load(temp / "overlay.json", 4096), True, delivery))
    def build():
        value = load(temp / "unpacked/BUILD.json", 2048)
        if (not isinstance(value, dict) or set(value) != {"schema", "variant", "sourceCommit", "features", "profile", "exeSha256", "distribution"}
                or value["schema"] != 1 or value["variant"] != "host-startup-diagnostic-1" or value["distribution"] is not False
                or value["features"] != ["custom-protocol", "startup-diagnostics"] or value["profile"] != "release"
                or not re.fullmatch("[0-9a-f]{40}", value["sourceCommit"]) or not re.fullmatch("[0-9a-f]{64}", value["exeSha256"])): raise ValueError()
        return value
    add("build", build)
    steps = files["steps"]["data"]
    native = files["native"]["data"]
    observation = files["observation"]["data"]
    requests = observation.get("sessionRequests", [])
    host_events = files["host"]["data"].get("events", [])
    stable = ("sourceCommit" in files["launch"]["data"] and files["launch"] == files["final"])
    passed = (isinstance(steps, list) and [row["stage"] for row in steps] == list(STEPS)
              and all(row["exitCode"] == 0 for row in steps)
              and native.get("nativeTestsRun") == native.get("nativeTestsPassed") == 1
              and native.get("normalCloseVerified") is True and native.get("cleanupFailures") == 0
              and native.get("cleanupStarted") is True and native.get("driverExitObservedAfterCleanup") is True
              and native.get("appStartupFileClosed") is True
              and native.get("childHandleDisposeFailures") == 0 and stable
              and observation.get("status") == "webview-passed-awaiting-close" and observation.get("phase") == "complete"
              and observation.get("errorClass") == "none" and observation.get("droppedRequests") == 0
              and type(observation.get("documentObservations")) is int and observation["documentObservations"] > 0
              and len(requests) == 2 and [row["outcome"] for row in requests] == ["started", "ok"]
              and all(row["elapsedMs"] is not None for row in requests) and requests[-1]["httpStatus"] == 200
              and "crateSha256" in files["overlay"]["data"]
              and files["build"]["data"].get("exeSha256") == files["launch"]["data"].get("exeSha256")
              and files["overlay"]["data"].get("overlayLockSha256") == files["launch"]["data"].get("overlayLockSha256")
              and len(files["wry"]["data"].get("events", [])) == 8
              and files["driver"]["data"].get("diagnostic") == "complete"
              and files["host"]["data"].get("diagnostic") == "complete"
              and files["host"]["data"].get("phase") == "first-page-load-finished"
              and len(host_events) == 5 and {row["stage"] for row in host_events} ==
                  {"setup-enter", "webview-build-start", "webview-build-ok", "setup-return", "first-page-load-finished"})
    control = os.environ.get("CONTROL_HEAD", "")
    if not re.fullmatch("[0-9a-f]{40}", control): raise ValueError()
    source = git(root, "rev-parse", "HEAD").decode().strip()
    tree = git(root, "rev-parse", "HEAD^{tree}").decode().strip()
    if not re.fullmatch("[0-9a-f]{40}", source) or not re.fullmatch("[0-9a-f]{40}", tree): raise ValueError()
    passed = passed and files["launch"]["data"].get("sourceCommit") == source and files["launch"]["data"].get("sourceTree") == tree
    passed = passed and files["build"]["data"].get("sourceCommit") == source
    if delivery:
        from wry_delivery_record import complete
        try: classified = complete(files["delivery"]["data"])
        except (ValueError, TypeError, KeyError): classified = False
        passed = passed and classified
    body = {"schema": 1, "variant": "wry240-once-record-1", "purpose": "diagnostic-hints-only",
            "productReference": "4ba768e910af595f26176eb69aaf2e21d663b726", "files": files,
            "controlCommit": control, "sourceCommit": source, "sourceTree": tree, "sourceAndSpecimenStable": stable,
            "allOriginalAndDiagnosticChecksPassed": passed,
            "rootCauseEstablished": False, "timeoutFixed": False}
    if delivery:
        body.update(variant="wry240-sdk-delivery-record-1", enabledScope="wry-sdk-delivery-1",
                    deliveryClassificationComplete=classified)
    encoded = canonical(body)
    if len(encoded) > 32768: raise ValueError("RECORD_LIMIT")
    text = encoded.decode()
    print("WRY_DIAGNOSTIC_RECORD_BEGIN\n" + text + "\nWRY_DIAGNOSTIC_RECORD_SHA256=" + digest(encoded)
          + "\nWRY_DIAGNOSTIC_RECORD_END")
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as stream:
            stream.write("```json\n" + text + "\n```\nSHA256 `" + digest(encoded) + "`\n")
    return passed


def main():
    try:
        delivery = os.environ.get("WRY_DELIVERY_ENABLED", "")
        if delivery not in ("", "1"): raise ValueError("DELIVERY_MODE_REJECTED")
        delivery = delivery == "1"
        parser = Parser(add_help=False)
        parser.add_argument("mode", choices=("step", "identity", "permissions", "publish"))
        parser.add_argument("--root", type=Path, required=True)
        parser.add_argument("--temp", type=Path, required=True)
        parser.add_argument("--stage", choices=STEPS)
        parser.add_argument("--exit", type=int)
        args = parser.parse_args()
        if args.mode == "step":
            if args.stage is None or args.exit is None or not 0 <= args.exit <= 65535: raise ValueError()
            path = args.temp / "steps.json"
            value = safe_steps(load(path, 4096)) if path.exists() else []
            if not isinstance(value, list) or len(value) >= len(STEPS) or any(row["stage"] == args.stage for row in value): raise ValueError()
            value.append({"stage": args.stage, "exitCode": args.exit})
            safe_steps(value)
            checkpoint(path, value)
        elif args.mode == "identity":
            permissions(args.root, True)
            value = source_identity(args.root, args.temp / "wry", args.temp / "wry.crate", delivery)
            if delivery and (value["sourceCommit"] != os.environ.get("WRY_PIN")
                             or value["sourceTree"] != os.environ.get("WRY_TREE")):
                raise ValueError("DELIVERY_SOURCE_PIN_REJECTED")
            exe = args.temp / "unpacked/rescue-host-shell.exe"
            if exe.read_bytes() != (args.temp / "fixture/rescue-host-shell.exe").read_bytes(): raise ValueError()
            if (args.temp / "unpacked/BUILD.json").read_bytes() != (args.temp / "fixture/BUILD.json").read_bytes(): raise ValueError()
            value["exeSha256"] = digest(exe.read_bytes())
            value["driverSha256"] = digest((args.temp / "tools/tauri/bin/tauri-driver.exe").read_bytes())
            value["archiveSha256"] = digest((args.temp / "fixture.zip").read_bytes())
            value["edgeDriverSha256"] = digest((args.temp / "tools/edge/msedgedriver.exe").read_bytes())
            value["driverPatchSha256"] = digest((args.root / "tools/native-driver-trace/driver.patch").read_bytes())
            value["driverLockSha256"] = digest((args.temp / "tools/tauri-driver-2.1.0/Cargo.lock").read_bytes())
            if args.stage not in ("fixture", "final-identity"): raise ValueError()
            path = args.temp / ("launch.json" if args.stage == "fixture" else "final.json")
            path.write_bytes(canonical(value))
            if args.stage == "final-identity" and path.read_bytes() != (args.temp / "launch.json").read_bytes(): raise ValueError()
        elif args.mode == "permissions":
            if args.stage is not None and (not delivery or args.stage != "build"): raise ValueError()
            permissions(args.root, args.stage == "build")
        else:
            return 0 if publish(args.root, args.temp, delivery) else 1
        return 0
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError):
        print('{"code":"WRY_DIAGNOSTIC_RECORD_REJECTED"}')
        return 1


if __name__ == "__main__": raise SystemExit(main())
