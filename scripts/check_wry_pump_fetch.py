"""Pre-publication admission: fetch the exact reviewed pin into an empty checkout.

Anonymous public HTTPS has repository-content read scope; the hosted ephemeral
GITHUB_TOKEN is neither available here nor needed. No credential is retained.
"""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

REMOTE = "https://github.com/O2-CONNECTIVE/rescue-host.git"
SOURCE = "cee2e9baa22200c929f015199aac68626925a486"
TREE = "ce3c61a0e56b2bec1fdc2a5990813dff17afb4ec"


def verify_workflow(text):
    for name, pin in (("WRY_PIN", SOURCE), ("WRY_TREE", TREE)):
        if re.findall(r"^      " + name + r": ([0-9a-f]{40})$", text, re.M) != [pin]:
            raise ValueError("WORKFLOW_PIN_REJECTED")
    if re.findall(r"        id: source\n        with:\n          ref: ([0-9a-f]{40})\n", text) != [SOURCE]:
        raise ValueError("WORKFLOW_PIN_REJECTED")


def fresh_fetch(remote, source, tree):
    if any(not re.fullmatch("[0-9a-f]{40}", value) for value in (source, tree)):
        raise ValueError("PIN_REJECTED")
    env = {k: v for k, v in os.environ.items()
           if not k.startswith("GIT_") and k not in {"NETRC", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE"}}
    env.update(GIT_TERMINAL_PROMPT="0", GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)
    with tempfile.TemporaryDirectory(prefix="wry-source-admission-") as directory:
        root = Path(directory) / "checkout"
        root.mkdir()
        home = Path(directory) / "home"
        home.mkdir()
        # libcurl can consult netrc independently of Git credential helpers.
        # An empty private home excludes both POSIX and Windows host homes.
        env.update(HOME=str(home), USERPROFILE=str(home), XDG_CONFIG_HOME=str(home),
                   HOMEDRIVE=home.drive, HOMEPATH=str(home)[len(home.drive):])
        def git(*args, timeout=15):
            result = subprocess.run(["git", "-c", "credential.helper=", "-c", "http.extraheader=",
                                     "-c", "protocol.version=2", "-C", str(root), *args],
                                    env=env, capture_output=True, timeout=timeout)
            if result.returncode: raise ValueError("GIT_REJECTED")
            return result.stdout.strip()
        git("init", "--quiet")
        if any((root / ".git/objects").rglob("*.pack")) or (root / ".git/objects/info/alternates").exists():
            raise ValueError("OBJECT_STORE_REJECTED")
        git("remote", "add", "origin", remote)
        # Same full-SHA shallow/no-tags/no-submodule fetch boundary as checkout.
        git("fetch", "--quiet", "--no-tags", "--prune", "--no-recurse-submodules", "--depth=1",
            "origin", source, timeout=90)
        if git("rev-parse", "FETCH_HEAD") != source.encode(): raise ValueError("COMMIT_REJECTED")
        if git("rev-parse", "FETCH_HEAD^{tree}") != tree.encode(): raise ValueError("TREE_REJECTED")
        if git("rev-parse", "--is-shallow-repository") != b"true": raise ValueError("DEPTH_REJECTED")
        if git("rev-list", "--count", "FETCH_HEAD") != b"1": raise ValueError("DEPTH_REJECTED")
        git("checkout", "--quiet", "--detach", "FETCH_HEAD")
        if git("rev-parse", "HEAD") != source.encode(): raise ValueError("CHECKOUT_REJECTED")
        git("diff", "--exit-code", "HEAD")
    return {"schema": 1, "purpose": "pre-publication-source-fetch", "source": source, "tree": tree,
            "freshEmptyCheckout": True, "depth": 1, "tags": False, "credentialScope": "anonymous-public-read",
            "verified": True}


def main():
    try:
        workflow = Path(__file__).resolve().parents[1] / ".github/workflows/pump-bracket-once.yml"
        verify_workflow(workflow.read_text())
        print(json.dumps(fresh_fetch(REMOTE, SOURCE, TREE), sort_keys=True, separators=(",", ":")))
        return 0
    except (OSError, ValueError, subprocess.SubprocessError):
        print('{"code":"WRY_SOURCE_FETCH_REJECTED"}')
        return 1


if __name__ == "__main__": raise SystemExit(main())
