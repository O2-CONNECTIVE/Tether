"""Run only fixed, tokenless Tether CI fixtures; zero/all-skipped is failure."""
import argparse
import json
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PACKAGE = "github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter/testdata/schemafixture"
NODE_FILES = (
    "deploy/cloudflare/probe/worker.test.mjs",
    "deploy/cloudflare/native/worker.test.mjs",
    "deploy/cloudflare/discovery/worker.test.mjs",
)
MAX_OUTPUT = 8 * 1024 * 1024


def go_count(text):
    runs, passes = set(), set()
    package_passes = 0
    for line in text.splitlines():
        event = json.loads(line)
        if not isinstance(event, dict) or event.get("Package") != PACKAGE:
            raise ValueError("unexpected Go package/event")
        action, name = event.get("Action"), event.get("Test")
        if action not in {"start", "run", "pause", "cont", "pass", "output"}:
            raise ValueError("failed/skipped/unknown Go event")
        if name is not None and (not isinstance(name, str) or not name):
            raise ValueError("invalid test name")
        if action == "run":
            if not name or name in runs:
                raise ValueError("duplicate/missing test run")
            runs.add(name)
        if action == "pass":
            if name:
                if name not in runs or name in passes:
                    raise ValueError("unmatched test pass")
                passes.add(name)
            else:
                package_passes += 1
    if not runs or runs != passes or package_passes != 1:
        raise ValueError("zero/incomplete Go tests")
    return len(passes)


def node_count(text):
    # Fixed node:test TAP totals, not arbitrary TAP input from a server.
    totals = {}
    for line in text.splitlines():
        match = re.fullmatch(r"# (tests|pass|fail|cancelled|skipped|todo) ([0-9]+)", line)
        if match:
            key = match[1]
            if key in totals:
                raise ValueError("duplicate TAP total")
            totals[key] = int(match[2])
        if re.match(r"\s*(not ok\b|Bail out!)", line):
            raise ValueError("TAP failure")
    if set(totals) != {"tests", "pass", "fail", "cancelled", "skipped", "todo"}:
        raise ValueError("missing TAP totals")
    if totals["tests"] <= 0 or totals["tests"] != totals["pass"] or any(totals[k] for k in ("fail", "cancelled", "skipped", "todo")):
        raise ValueError("zero/skipped/failed TAP tests")
    return totals["pass"]


def run_one(args, cwd, parser):
    # File capture avoids unbounded Python memory. Execution has a finite timeout;
    # only our own fixture subprocess is terminated by subprocess.run on timeout.
    # Do not print raw fixture output into a generic credential-bearing CI log.
    with tempfile.TemporaryFile() as output:
        result = subprocess.run(args, cwd=cwd, stdout=output, stderr=subprocess.STDOUT, timeout=90, check=False)
        size = output.tell()
        if result.returncode != 0 or size > MAX_OUTPUT:
            raise ValueError("fixture failed/oversized")
        output.seek(0)
        return parser(output.read(MAX_OUTPUT + 1).decode("utf-8", errors="strict"))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("suite", choices=("go-schema", "workers"))
    args = parser.parse_args(argv)
    try:
        if args.suite == "go-schema":
            count = run_one(["go", "test", "-mod=readonly", "-json", "-count=1", "-timeout=60s", "./internal/mcpadapter/testdata/schemafixture"], ROOT / "backend", go_count)
        else:
            count = 0
            for name in NODE_FILES:
                if not (ROOT / name).is_file():
                    raise ValueError("missing fixed Worker suite")
                count += run_one(["node", "--test", "--test-reporter=tap", name], ROOT, node_count)
        print("TETHER_FIXTURES suite=" + args.suite + " passed=" + str(count) + " skipped=0")
        return 0
    except (OSError, ValueError, subprocess.TimeoutExpired):
        print("TETHER_FIXTURES failed")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
