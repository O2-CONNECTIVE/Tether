#!/usr/bin/env python3
"""Fixed CI-only synthetic checks. No agent, network, secret or arbitrary command API."""
import hashlib
import json
import os
from pathlib import Path
import re
import struct
import subprocess
import sys

MAX_LOG = 1024 * 1024
IMAGES = {
    "connected-light.png": (1120, 1240), "connected-dark.png": (1120, 1240),
    "cleanup-unknown.png": (1120, 1400), "cleanup-unknown-dark.png": (1120, 1400),
    "terminal-closed.png": (1120, 1400), "not-started.png": (1120, 1400),
    "stale-narrow.png": (720, 1520), "large-text.png": (1120, 1800),
}
STEPS = {
    "source": [
        ("build", ["xcrun", "swift", "build"], 180, None),
        ("core", ["xcrun", "swift", "run", "StatusCoreChecks"], 60, b"CHECKS 21 FAILURES 0"),
        ("package-tests", ["python3", "-m", "unittest", "discover", "-s", "Tests",
                           "-p", "test_packaging.py", "-v"], 60, b"Ran 10 tests"),
        ("package", ["python3", "scripts/package_local.py"], 30,
         b"PACKAGE_LOCAL_EVALUATION_READY"),
    ],
    "gui": [
        ("window", [".build/debug/StatusWindowChecks"], 15, b"OWN_WINDOW_CHECKS 9 FAILURES 0"),
        ("bundle-no-input", [".build/local-evaluation/Tether Status.app/Contents/MacOS/TetherStatus",
                             "--check-own-window-no-input"], 15, b"OWN_BUNDLE_WINDOW_PASS"),
        ("bundle-fixture", [".build/local-evaluation/Tether Status.app/Contents/MacOS/TetherStatus",
                            "--check-own-window-fixture"], 15, b"OWN_BUNDLE_WINDOW_PASS"),
        ("render", [".build/debug/TetherStatus", "--render-fixtures"], 30, None),
    ],
}

def hosted_environment(env):
    return (env.get("GITHUB_ACTIONS") == "true" and env.get("RUNNER_ENVIRONMENT") == "github-hosted"
            and env.get("RUNNER_OS") == "macOS"
            and env.get("RUNNER_ARCH") == "ARM64"
            and env.get("DEVELOPER_DIR") == "/Applications/Xcode_26.5.app/Contents/Developer")

def run_step(root, evidence, spec, popen=subprocess.Popen):
    name, argv, seconds, expected = spec
    result = {"step": name, "result": "failed", "exit_code": None, "reaped": False}
    path = evidence / (name + ".log")
    try:
        # Fixed source tools only. Log is not uploaded; never dump inherited environment.
        with path.open("xb") as log, open(os.devnull, "rb") as stdin:
            child = popen(argv, cwd=root, stdin=stdin, stdout=log, stderr=log)
            try:
                result["exit_code"] = child.wait(timeout=seconds)
                result["reaped"] = True
            except subprocess.TimeoutExpired:
                # No force kill, no success inference. Stop suite; hosted job owns VM disposal.
                result["result"] = "timeout_child_unconfirmed"
                result["owned_pid"] = child.pid
                return result
        if path.stat().st_size > MAX_LOG:
            result["result"] = "output_bound"
            return result
        content = path.read_bytes()
        if result["exit_code"] != 0:
            return result
        if expected is not None and len(re.findall(rb"(?m)^" + re.escape(expected) + rb"(?: in [^\r\n]+)?\r?$", content)) != 1:
            result["result"] = "missing_or_duplicate_success"
            return result
        if b"FAILURES 1" in content or b"OWN_BUNDLE_WINDOW_TIMEOUT" in content or b"skipped=" in content:
            result["result"] = "unexpected_result"
            return result
        result["result"] = "passed"
        return result
    except OSError:
        result["result"] = "process_or_log_error"
        return result

def inspect_images(directory):
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError("image directory")
    if set(p.name for p in directory.iterdir()) != set(IMAGES):
        raise ValueError("exact image set")
    results = []
    for name, expected in sorted(IMAGES.items()):
        path = directory / name
        if path.is_symlink() or not path.is_file() or not 1000 < path.stat().st_size <= 10*1024*1024:
            raise ValueError("image bound")
        data = path.read_bytes()
        if data[:8] != b"\x89PNG\r\n\x1a\n" or data[12:16] != b"IHDR" or struct.unpack(">II", data[16:24]) != expected:
            raise ValueError("PNG dimensions")
        results.append({"name": name, "sha256": hashlib.sha256(data).hexdigest(), "bytes": len(data)})
    return results

def run_suite(root, mode, step_runner=run_step):
    evidence = root / ".build/ci-evidence"
    if evidence.is_symlink():
        raise ValueError("evidence boundary")
    evidence.mkdir(parents=True, exist_ok=True)
    report = {"mode": mode, "steps": [], "images": [], "result": "failed"}
    for spec in STEPS[mode]:
        result = step_runner(root, evidence, spec)
        report["steps"].append(result)
        if result["result"] != "passed":
            break
    else:
        try:
            if mode == "gui":
                report["images"] = inspect_images(root / ".build/fixture-images")
            report["result"] = "passed"
        except (OSError, ValueError, struct.error):
            report["result"] = "image_validation_failed"
    with (evidence / (mode + ".json")).open("x") as stream:
        json.dump(report, stream, indent=2)
        stream.write("\n")
    return report["result"] == "passed"

def main(args, env):
    if args not in (["source"], ["gui"]) or not hosted_environment(env):
        print("CI_SCOPE_REFUSED")
        return 64
    try:
        passed = run_suite(Path(__file__).resolve().parents[1], args[0])
    except (OSError, ValueError, KeyError):
        print("CI_CHECK_FAILED")
        return 1
    print("CI_CHECK_PASS" if passed else "CI_CHECK_FAILED")
    return 0 if passed else 1

if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:], os.environ))
