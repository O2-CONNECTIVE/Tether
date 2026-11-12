#!/usr/bin/env python3
"""Read-only development artifact comparison; never grants launch or trust."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import stat
import sys
from xml.parsers.expat import ExpatError

FILES = {
    "gui": "Contents/MacOS/TetherDevelopment",
    "resident": "Contents/MacOS/tether-dev-resident",
    "plist": "Contents/Info.plist",
    "resident_digest": "Contents/Resources/resident.sha256",
}
DIRECTORIES = {"Contents", "Contents/MacOS", "Contents/Resources"}
SCHEMA = "tether-internal-local-build/1"


def refuse():
    raise ValueError("preflight-refused")


def regular_bytes(path, limit):
    # Reject symlink ancestors as well as the leaf. No resolve() before checks.
    path = Path(os.path.abspath(path))
    for ancestor in reversed(path.parents):
        if not stat.S_ISDIR(ancestor.lstat().st_mode):
            refuse()
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or not 0 < before.st_size <= limit:
        refuse()
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
    fd = os.open(path, flags)
    with os.fdopen(fd, "rb") as stream:
        opened = os.fstat(stream.fileno())
        if (opened.st_dev, opened.st_ino) != (before.st_dev, before.st_ino):
            refuse()
        data = stream.read(limit + 1)
        after = os.fstat(stream.fileno())
    if len(data) != before.st_size or len(data) > limit or (after.st_size, after.st_mtime_ns, after.st_ctime_ns) != (opened.st_size, opened.st_mtime_ns, opened.st_ctime_ns):
        refuse()
    return data


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            refuse()
        result[key] = value
    return result


def inspect(app, record, expected_source):
    if not re.fullmatch(r"[0-9a-f]{40}", expected_source):
        refuse()
    manifest = json.loads(regular_bytes(record, 4096), object_pairs_hook=unique_object)
    if not isinstance(manifest, dict) or set(manifest) != {"schema", "source_commit", "profile", "sha256"}:
        refuse()
    if manifest["schema"] != SCHEMA or manifest["profile"] != "development-local-only":
        refuse()
    if manifest["source_commit"] != expected_source:
        refuse()
    hashes = manifest["sha256"]
    if not isinstance(hashes, dict) or set(hashes) != set(FILES):
        refuse()
    if any(not isinstance(v, str) or not re.fullmatch(r"[0-9a-f]{64}", v) for v in hashes.values()):
        refuse()
    root = Path(os.path.abspath(app))
    for path in (root, *root.parents):
        if not stat.S_ISDIR(path.lstat().st_mode):
            refuse()
    entries = set()
    # Fixed-depth traversal rejects links/unknown directories without descending.
    pending = [(root, "")]
    while pending:
        folder, prefix = pending.pop()
        for entry in folder.iterdir():
            relative = prefix + entry.name
            entries.add(relative)
            mode = entry.lstat().st_mode
            if relative in DIRECTORIES and stat.S_ISDIR(mode):
                pending.append((entry, relative + "/"))
            elif relative not in FILES.values() or not stat.S_ISREG(mode):
                refuse()
    if entries != DIRECTORIES | set(FILES.values()):
        refuse()
    contents = {}
    for role, relative in FILES.items():
        limit = 128 * 1024 * 1024 if role in ("gui", "resident") else 16384
        contents[role] = regular_bytes(root / relative, limit)
        if hashlib.sha256(contents[role]).hexdigest() != hashes[role]:
            refuse()
    plist = plistlib.loads(contents["plist"])
    if not isinstance(plist, dict) or plist.get("CFBundleIdentifier") != "com.o2connective.tether.development.gui" or plist.get("CFBundleExecutable") != "TetherDevelopment":
        refuse()
    if contents["resident_digest"] != (hashes["resident"] + "\n").encode("ascii"):
        refuse()
    return {"schema": SCHEMA, "status": "development-bytes-match", "source_commit": expected_source,
            "sha256": hashes, "profile": "development-local-only", "launch_authorized": False,
            "publisher_verified": False, "source_binary_provenance": "not-established",
            "production_ready": False, "distribution_authorized": False}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--app", required=True)
    parser.add_argument("--record", required=True)
    parser.add_argument("--source-commit", required=True)
    args = parser.parse_args(argv)
    try:
        result = inspect(args.app, args.record, args.source_commit)
    except (OSError, ValueError, TypeError, OverflowError, RecursionError, plistlib.InvalidFileException, ExpatError):
        print("preflight-refused", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
