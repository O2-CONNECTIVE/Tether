"""Real independent bare-remote fetch: omitted sibling red, published sibling green."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import check_wry_pump_fetch as admission


class FetchTests(unittest.TestCase):
    def test_actual_workflow_pins_and_missing_changed_duplicate_pins(self):
        text = (Path(__file__).resolve().parents[2] / ".github/workflows/pump-bracket-once.yml").read_text()
        admission.verify_workflow(text)
        for name, pin in (("WRY_PIN", admission.SOURCE), ("WRY_TREE", admission.TREE)):
            line = "      " + name + ": " + pin + "\n"
            for bad in (text.replace(line, ""), text.replace(line, line + line), text.replace(line, line.replace(pin, "0" * 40))):
                with self.assertRaises(ValueError): admission.verify_workflow(bad)
        with self.assertRaises(ValueError): admission.verify_workflow(text.replace("          ref: " + admission.SOURCE, "          ref: " + "1" * 40))

    def test_actual_omitted_sibling_then_published_exact_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); source = root / "source"; remote = root / "remote.git"
            def git(*args):
                return subprocess.check_output(["git", "-C", str(source), *args], stderr=subprocess.DEVNULL, timeout=15).strip().decode()
            source.mkdir(); git("init", "--quiet")
            git("config", "user.name", "fixture"); git("config", "user.email", "fixture@example.invalid")
            (source / "fixed.txt").write_bytes(b"base\n"); git("add", "."); git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            git("checkout", "-qb", "source-pin")
            (source / "fixed.txt").write_bytes(b"reviewed\n"); git("commit", "-qam", "source")
            pin, tree = git("rev-parse", "HEAD"), git("rev-parse", "HEAD^{tree}")
            git("checkout", "-qb", "control", base)
            (source / "control.txt").write_bytes(b"control\n"); git("add", "."); git("commit", "-qm", "control")
            subprocess.run(["git", "init", "--bare", "--quiet", str(remote)], check=True, capture_output=True, timeout=15)
            git("push", "--quiet", str(remote), "HEAD:refs/heads/control")
            with self.assertRaises(ValueError): admission.fresh_fetch(remote.as_uri(), pin, tree)
            git("push", "--quiet", str(remote), "source-pin:refs/heads/source-pin")
            # Hostile inherited object/config settings must not contaminate the empty checkout.
            host_home = root / "host-home"; host_home.mkdir()
            (host_home / ".netrc").write_text("machine fixture.invalid login PRIVATE_SENTINEL password PRIVATE_SENTINEL\n")
            real_run = subprocess.run
            checked = []
            def isolated_run(*args, **kwargs):
                env = kwargs["env"]; home = Path(env["HOME"])
                self.assertNotEqual(home, host_home)
                self.assertTrue(home.is_dir()); self.assertEqual(list(home.iterdir()), [])
                self.assertEqual(env["USERPROFILE"], str(home))
                self.assertEqual(env["XDG_CONFIG_HOME"], str(home))
                self.assertEqual(env["HOMEDRIVE"] + env["HOMEPATH"], str(home))
                for name in ("NETRC", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "GIT_ASKPASS", "GIT_DIR", "GIT_ALTERNATE_OBJECT_DIRECTORIES"):
                    self.assertNotIn(name, env)
                checked.append(True)
                return real_run(*args, **kwargs)
            hostile = {"GIT_DIR": str(source / ".git"), "GIT_ALTERNATE_OBJECT_DIRECTORIES": str(source / ".git/objects"),
                       "HOME": str(host_home), "USERPROFILE": str(host_home), "XDG_CONFIG_HOME": str(host_home),
                       "NETRC": str(host_home / ".netrc"), "SSH_ASKPASS": "PRIVATE_SENTINEL", "GIT_ASKPASS": "PRIVATE_SENTINEL"}
            with patch.dict(os.environ, hostile), patch.object(subprocess, "run", side_effect=isolated_run):
                result = admission.fresh_fetch(remote.as_uri(), pin, tree)
            self.assertGreater(len(checked), 0)
            self.assertTrue(result["verified"]); self.assertEqual(result["source"], pin)
            with self.assertRaises(ValueError): admission.fresh_fetch(remote.as_uri(), pin, "0" * 40)
            with self.assertRaises(ValueError): admission.fresh_fetch(remote.as_uri(), "0" * 40, tree)

    def test_wrong_pin_shape_never_runs_git(self):
        with patch.object(subprocess, "run", side_effect=AssertionError("GIT_MUST_NOT_RUN")):
            for source in ("", "x" * 40, "a" * 39, "a" * 40 + "PRIVATE_SENTINEL"):
                with self.assertRaises(ValueError): admission.fresh_fetch("unused", source, "0" * 40)

    def test_rejected_cli_never_copies_subprocess_output(self):
        for error in (ValueError("PRIVATE_SENTINEL"), OSError("PRIVATE_SENTINEL"), subprocess.TimeoutExpired("PRIVATE_SENTINEL", 90)):
            import contextlib, io
            output = io.StringIO()
            with patch.object(admission, "fresh_fetch", side_effect=error), contextlib.redirect_stdout(output):
                self.assertEqual(admission.main(), 1)
            self.assertEqual(output.getvalue(), '{"code":"WRY_SOURCE_FETCH_REJECTED"}\n')


if __name__ == "__main__": unittest.main()
