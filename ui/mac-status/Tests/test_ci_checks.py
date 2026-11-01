import importlib.util
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("ci_checks", Path(__file__).with_name("run_ci_checks.py"))
ci = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ci)

class Child:
    pid = 123
    def __init__(self, code=0, timeout=False):
        self.code, self.timeout = code, timeout
    def wait(self, timeout):
        if self.timeout: raise subprocess.TimeoutExpired("synthetic", timeout)
        return self.code

class CIChecks(unittest.TestCase):
    def execute(self, content=b"CHECKS 21 FAILURES 0\n", code=0, timeout=False):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp)
            def fake(argv, **kw):
                self.assertEqual(argv, ["synthetic"])
                self.assertNotIn("shell", kw)
                kw["stdout"].write(content)
                return Child(code, timeout)
            return ci.run_step(root, root, ("fixture", ["synthetic"], 1, b"CHECKS 21 FAILURES 0"), fake)

    def test_exact_success(self):self.assertEqual(self.execute()["result"],"passed")
    def test_nonzero_not_success(self):self.assertEqual(self.execute(code=1)["result"],"failed")
    def test_missing_success(self):self.assertEqual(self.execute(b"")["result"],"missing_or_duplicate_success")
    def test_duplicate_success(self):self.assertEqual(self.execute(b"CHECKS 21 FAILURES 0\n"*2)["result"],"missing_or_duplicate_success")
    def test_timeout_not_reaped_or_retried(self):
        result=self.execute(timeout=True)
        self.assertEqual(result["result"],"timeout_child_unconfirmed")
        self.assertFalse(result["reaped"])
    def test_output_bound(self):self.assertEqual(self.execute(b"x"*(ci.MAX_LOG+1))["result"],"output_bound")
    def test_scope(self):
        env={"GITHUB_ACTIONS":"true","RUNNER_ENVIRONMENT":"github-hosted","RUNNER_OS":"macOS","RUNNER_ARCH":"ARM64","DEVELOPER_DIR":"/Applications/Xcode_26.5.app/Contents/Developer"}
        self.assertTrue(ci.hosted_environment(env))
        for key in env:
            invalid=dict(env);invalid[key]="other";self.assertFalse(ci.hosted_environment(invalid))
    def test_local_run_refused(self):self.assertEqual(ci.main(["source"],{}),64)
    def test_unknown_mode_refused(self):self.assertEqual(ci.main(["other"],{}),64)
    def test_suite_stops_after_first_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            calls=[]
            def fake(root,evidence,step):
                calls.append(step[0]);return {"result":"failed"}
            self.assertFalse(ci.run_suite(Path(temp),"source",fake))
            self.assertEqual(calls,["build"])
    def test_images_exact_set_dimensions(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp)
            for name,dimensions in ci.IMAGES.items():
                (root/name).write_bytes(b"\x89PNG\r\n\x1a\n"+struct.pack(">I",13)+b"IHDR"+struct.pack(">II",*dimensions)+b"x"*1100)
            self.assertEqual(len(ci.inspect_images(root)),8)
            (root/"extra.png").write_bytes(b"x")
            with self.assertRaises(ValueError):ci.inspect_images(root)
    def test_invalid_png_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp)
            for name in ci.IMAGES:(root/name).write_bytes(b"x"*1100)
            with self.assertRaises(ValueError):ci.inspect_images(root)

if __name__=="__main__":unittest.main()
