import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("tether_fixtures", Path(__file__).resolve().parents[1] / "check_tether_fixtures.py")
m = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(m)


def go(events):
    return "\n".join(json.dumps(dict(Package=m.PACKAGE, **e)) for e in events)


def tap(n=2, **changes):
    totals = dict(tests=n, **{"pass": n}, fail=0, cancelled=0, skipped=0, todo=0)
    totals.update(changes)
    return "\n".join("# %s %s" % item for item in totals.items())


class FixtureTests(unittest.TestCase):
    def test_go_success(self):
        self.assertEqual(m.go_count(go([dict(Action="run", Test="TestA"), dict(Action="pass", Test="TestA"), dict(Action="pass")])), 1)

    def test_go_rejects_zero_skips_fail_incomplete_and_duplicates(self):
        cases = [[], [dict(Action="pass")], [dict(Action="skip", Test="TestA")], [dict(Action="fail")], [dict(Action="run", Test="TestA"), dict(Action="pass")], [dict(Action="run", Test="TestA")]*2, [dict(Action="pass", Test="TestA")]]
        for events in cases:
            with self.subTest(events=events), self.assertRaises(ValueError): m.go_count(go(events))
        for text in ['no JSON', '{}', '[]', go([dict(Action="start")]).replace(m.PACKAGE, "other")]:
            with self.subTest(text=text), self.assertRaises(ValueError): m.go_count(text)

    def test_node_success(self):
        self.assertEqual(m.node_count(tap()), 2)

    def test_node_rejects_zero_skipped_failed_incomplete_duplicate(self):
        for text in [tap(0), tap(skipped=1), tap(fail=1), tap(cancelled=1), tap(todo=1), tap(**{"pass": 1}), tap()+"\n# tests 2", "", tap()+"\nnot ok 1", tap()+"\nBail out! error"]:
            with self.subTest(text=text), self.assertRaises(ValueError): m.node_count(text)

    def test_fixed_workers_all_run(self):
        with patch.object(m, "run_one", return_value=1) as run:
            self.assertEqual(m.main(["workers"]), 0)
        self.assertEqual([c.args[0][-1] for c in run.call_args_list], list(m.NODE_FILES))

    def test_fixed_go_args(self):
        with patch.object(m, "run_one", return_value=1) as run:
            self.assertEqual(m.main(["go-schema"]), 0)
        self.assertEqual(run.call_args.args[0][-1], "./internal/mcpadapter/testdata/schemafixture")
        self.assertIn("-json", run.call_args.args[0])

    def test_command_error_timeout_refused(self):
        for error in [ValueError("fake"), OSError("fake"), subprocess.TimeoutExpired("fixture", 90)]:
            with patch.object(m, "run_one", side_effect=error): self.assertEqual(m.main(["go-schema"]), 1)

    def test_nonzero_and_oversize_refused(self):
        for returncode, data in [(1,b''),(0,b'x'*9)]:
            def fake(args, **kw):
                kw["stdout"].write(data)
                return subprocess.CompletedProcess(args, returncode)
            with patch.object(m.subprocess, "run", side_effect=fake), patch.object(m, "MAX_OUTPUT", 8), self.assertRaises(ValueError):
                m.run_one(["fixture"], m.ROOT, m.node_count)


if __name__ == "__main__": unittest.main()
