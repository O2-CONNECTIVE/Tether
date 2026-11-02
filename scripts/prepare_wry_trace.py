#!/usr/bin/env python3
"""Prepare an isolated Wry diagnostic build input; never resolve or build."""
import argparse
import hashlib
import io
import json
import os
import posixpath
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import tarfile

ARCHIVE_SHA256 = "759c50ddad0ef9913bb5496cb9d509a50433a3b78cb0238d61105526589cf0e9"
BUNDLE_SHA256 = "e317bd6136cbe3acab4e27837e04942d7221eb4740221b8859414bc3c3ac2c22"
VERSION = "0.57.1"
APP_PREFIX = "ui/tauri/src-tauri/"
CONTRACTS = {
    "contracts/control-v1/vectors.json": "contracts/control-v1/vectors.json",
    "ui/tauri/contracts/shell-status-v1.json": "contracts/shell-status-v1.json",
}
BUNDLE_FILES = ("patch.json", "startup_trace.rs", "app_hook.rs.txt")
MAX_BYTES = 64 * 1024 * 1024


def digest(data):
    return hashlib.sha256(data).hexdigest()


def tree_digest(files):
    hashes = {name: digest(data) for name, data in sorted(files.items())}
    return digest(json.dumps(hashes, sort_keys=True, separators=(",", ":")).encode())


def require(condition, message):
    if not condition:
        raise ValueError(message)


def safe_name(name):
    path = PurePosixPath(name)
    require(name and not path.is_absolute() and "\\" not in name
            and ":" not in name and all(p not in ("", ".", "..") for p in name.split("/")),
            "unsafe relative path")
    return path


def no_symlink(path):
    path = Path(os.path.abspath(str(path)))
    for part in (path,) + tuple(path.parents):
        require(not part.is_symlink(), "symlink input/output is forbidden")
    return path


def read_regular(path):
    path = no_symlink(path)
    require(path.is_file() and stat.S_ISREG(path.stat().st_mode), "regular file required")
    require(path.stat().st_size <= MAX_BYTES, "input too large")
    return path.read_bytes()


def read_archive(archive):
    data = read_regular(archive)
    require(digest(data) == ARCHIVE_SHA256, "Wry archive checksum mismatch")
    files = {}
    total = 0
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as stream:
        members = stream.getmembers()
        require(len(members) <= 4096, "too many archive members")
        seen = set()
        for member in members:
            path = safe_name(member.name)
            require(path.parts[0] == "wry-" + VERSION, "unexpected archive root")
            require(member.name not in seen, "duplicate archive member")
            seen.add(member.name)
            require(member.isdir() or member.isfile(), "archive links/special files forbidden")
            if member.isdir():
                continue
            require(len(path.parts) > 1, "invalid archive file")
            total += member.size
            require(0 <= member.size <= MAX_BYTES and total <= MAX_BYTES, "archive too large")
            name = "/".join(path.parts[1:])
            files[name] = stream.extractfile(member).read()
    require("Cargo.toml" in files and "src/lib.rs" in files, "incomplete archive")
    return files


def apply_patch(files, bundle):
    require(set(bundle) == set(BUNDLE_FILES) and tree_digest(bundle) == BUNDLE_SHA256,
            "diagnostic patch bundle checksum mismatch")
    spec = json.loads(bundle["patch.json"])
    require(spec["schema"] == 1 and spec["crate"] == "wry"
            and spec["version"] == VERSION
            and spec["archiveSha256"] == ARCHIVE_SHA256, "unsupported patch")
    require([entry["path"] for entry in spec["files"]] ==
            ["src/lib.rs", "src/webview2/mod.rs"], "unexpected patch targets")
    result = dict(files)
    for entry in spec["files"]:
        name = entry["path"]
        require(digest(result[name]) == entry["beforeSha256"], "patch preimage mismatch")
        text = result[name].decode("utf-8")
        for edit in entry["replacements"]:
            start = text.index(edit["regionStart"]) if "regionStart" in edit else 0
            end = text.index(edit["regionEnd"], start) if "regionEnd" in edit else len(text)
            region = text[start:end]
            require(region.count(edit["before"]) == 1, "patch anchor mismatch")
            text = text[:start] + region.replace(edit["before"], edit["after"], 1) + text[end:]
        result[name] = text.encode()
        require(digest(result[name]) == entry["afterSha256"], "patch postimage mismatch")
    require("src/startup_trace.rs" not in result, "unexpected existing hook")
    result["src/startup_trace.rs"] = bundle["startup_trace.rs"]
    return result


def replace_once(text, old, new):
    require(text.count(old) == 1, "source anchor mismatch")
    return text.replace(old, new, 1)


def prepare_lock(text):
    # Deliberately not a TOML resolver: edit two exact known package blocks,
    # retaining every other byte/version/checksum. Cargo --locked is a later gate.
    chunks = text.split("[[package]]\n")
    require(len(chunks) > 2, "unsupported lock format")
    names = [re.search(r'^name = "([^"]+)"$', chunk, re.M) for chunk in chunks[1:]]
    require(sum(m is not None and m[1] == "wry" for m in names) == 1, "ambiguous Wry lock")
    require(sum(m is not None and m[1] == "rescue-host-shell" for m in names) == 1,
            "ambiguous app lock")
    for index in range(1, len(chunks)):
        chunk = chunks[index]
        if chunk.startswith('name = "wry"\n'):
            old = ('name = "wry"\nversion = "' + VERSION + '"\n'
                   'source = "registry+https://github.com/rust-lang/crates.io-index"\n'
                   'checksum = "' + ARCHIVE_SHA256 + '"\n')
            chunks[index] = replace_once(chunk, old, 'name = "wry"\nversion = "' + VERSION + '"\n')
        elif chunk.startswith('name = "rescue-host-shell"\n'):
            require('\n "wry' not in chunk, "app already depends on Wry")
            require(chunk.count("dependencies = [\n") == 1, "app dependencies missing")
            chunks[index] = replace_once(chunk, "\n]\n", '\n "wry",\n]\n')
    # A version-qualified Wry edge would need a different lock edit: fail closed.
    require(not re.search(r'^ "wry [^"]+"', text, re.M), "qualified Wry edge unsupported")
    return "[[package]]\n".join(chunks)


def verify_includes(files):
    """Fail closed on compile-time includes outside the copied app input."""
    macro = r"\binclude(?:_str|_bytes)?!\s*\("
    literal = macro + r'\s*"([^"]+)"\s*,?\s*\)'
    for name, data in files.items():
        if not name.endswith(".rs"):
            continue
        text = data.decode()
        includes = re.findall(literal, text)
        require(len(includes) == len(re.findall(macro, text)), "nonliteral include unsupported")
        for target in includes:
            require(not target.startswith("/") and "\\" not in target
                    and ":" not in target, "invalid include target")
            resolved = posixpath.normpath(posixpath.join(posixpath.dirname(name), target))
            safe_name(resolved)
            require(resolved in files, "include target outside prepared inputs")


def prepare_app(files, bridge):
    result = dict(files)
    manifest = result["src-tauri/Cargo.toml"].decode().replace("\r\n", "\n")
    require("[patch." not in manifest and not re.search(r"^wry\s*=", manifest, re.M),
            "manifest already overrides Wry")
    manifest = replace_once(manifest, "[target.'cfg(windows)'.dependencies]\n",
                            "[target.'cfg(windows)'.dependencies]\n"
                            'wry = { version = "=0.57.1", default-features = false }\n')
    manifest += '\n[patch.crates-io]\nwry = { path = "../../wry" }\n'
    result["src-tauri/Cargo.toml"] = manifest.encode()
    result["src-tauri/Cargo.lock"] = prepare_lock(result["src-tauri/Cargo.lock"].decode().replace("\r\n", "\n")).encode()
    name = "src-tauri/src/startup_diagnostics.rs"
    trace = result[name].decode().replace("\r\n", "\n")
    require("wry::startup_trace" not in trace and "wry_hook: AtomicBool" in trace,
            "diagnostic recorder missing or already hooked")
    marker = "        let recorder = Recorder::memory();\n"
    result[name] = replace_once(trace, marker, marker + bridge.decode()).encode()
    old_include = b"../../../../contracts/control-v1/vectors.json"
    new_include = b"../../contracts/control-v1/vectors.json"
    for name, count in (("src-tauri/tests/control_contract.rs", 3),
                        ("src-tauri/tests/control_coordinator.rs", 1)):
        require(result[name].count(old_include) == count, "contract include preimage mismatch")
        result[name] = result[name].replace(old_include, new_include)
    verify_includes(result)
    return result


def collect_app(source):
    listing = subprocess.run(["git", "-C", str(source), "ls-files", "-z", "--",
                              APP_PREFIX, *CONTRACTS],
                             check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout
    names = listing.decode("utf-8").split("\0")
    require(set(CONTRACTS).issubset(names), "tracked contract inputs missing")
    files = {}
    for name in filter(None, names):
        safe_name(name)
        if name in CONTRACTS:
            files[CONTRACTS[name]] = read_regular(source / name)
            continue
        require(name.startswith(APP_PREFIX), "unexpected tracked path")
        relative = name[len(APP_PREFIX):]
        parts = safe_name(relative).parts
        require(not any(p.startswith(".") or p in ("target", "node_modules", "gen")
                        or p.lower().endswith((".pem", ".key", ".p12", ".pfx"))
                        for p in parts), "private/generated tracked input forbidden")
        files["src-tauri/" + relative] = read_regular(source / name)
    require(files, "no tracked app source")
    dist = no_symlink(source / "ui/tauri/dist")
    require(dist.is_dir(), "generated frontend dist missing")
    allowed = {".html", ".js", ".css", ".svg", ".png", ".ico", ".jpg", ".jpeg",
               ".webp", ".woff", ".woff2", ".ttf", ".txt"}
    for directory, dirs, names in os.walk(str(dist), followlinks=False):
        for name in dirs + names:
            path = no_symlink(Path(directory) / name)
            require(not name.startswith("."), "hidden dist entry forbidden")
            if name in names:
                require(path.suffix.lower() in allowed, "unexpected dist artifact")
                relative = path.relative_to(dist).as_posix()
                safe_name(relative)
                files["dist/" + relative] = read_regular(path)
    require("dist/index.html" in files, "frontend index missing")
    require(sum(map(len, files.values())) <= MAX_BYTES, "app input too large")
    return files


def prepare(source, archive, output, bundle_dir=None):
    source = no_symlink(source).resolve()
    output = no_symlink(output)
    require(not output.exists(), "output must not exist")
    require(source != output and source not in output.parents and output not in source.parents,
            "output must be outside source tree")
    require(output.parent.is_dir(), "output parent must exist")
    bundle_dir = bundle_dir or Path(__file__).resolve().parent.parent / "tools/wry-startup-trace"
    bundle = {name: read_regular(Path(bundle_dir) / name) for name in BUNDLE_FILES}
    wry = apply_patch(read_archive(archive), bundle)
    app = prepare_app(collect_app(source), bundle["app_hook.rs.txt"])
    provenance = {
        "schema": 1, "variant": "wry-startup-trace-1", "wryVersion": VERSION,
        "archiveSha256": ARCHIVE_SHA256, "patchSha256": tree_digest(bundle),
        "preparedSourceSha256": tree_digest(app), "wrySourceSha256": tree_digest(wry),
        "generatedLockSha256": digest(app["src-tauri/Cargo.lock"]),
    }
    # All validation precedes creation; mkdir refuses existing outputs.
    output.mkdir()
    try:
        for prefix, files in (("app", app), ("wry", wry)):
            for name, data in files.items():
                safe_name(name)
                path = output / prefix / name
                path.parent.mkdir(parents=True, exist_ok=True)
                with path.open("xb") as stream:
                    stream.write(data)
        inventory = {prefix: {name: digest(data) for name, data in files.items()}
                     for prefix, files in (("app", app), ("wry", wry))}
        with (output / "prepared-files.json").open("x") as stream:
            json.dump(inventory, stream, sort_keys=True, indent=2)
            stream.write("\n")
        with (output / "provenance.json").open("x") as stream:
            json.dump(provenance, stream, sort_keys=True, indent=2)
            stream.write("\n")
    except BaseException:
        shutil.rmtree(output)
        raise
    return provenance


def verify_graph(data, output):
    """Check Cargo's resolved IDs, not names alone; metadata is read-only input."""
    packages = data["packages"]
    wry = [p for p in packages if p["name"] == "wry"]
    require(len(wry) == 1, "multiple/missing Wry instances")
    package = wry[0]
    require(package["version"] == VERSION and package["source"] is None
            and Path(package["manifest_path"]).resolve() ==
            (output / "wry/Cargo.toml").resolve(), "wrong Wry instance")
    roots = [p for p in packages if p["id"] == data["resolve"]["root"]]
    require(len(roots) == 1 and roots[0]["name"] == "rescue-host-shell"
            and Path(roots[0]["manifest_path"]).resolve() ==
            (output / "app/src-tauri/Cargo.toml").resolve(), "wrong root package")
    runtime = [p for p in packages if p["name"] == "tauri-runtime-wry"]
    require(len(runtime) == 1, "multiple/missing Tauri Wry runtime")
    nodes = {node["id"]: node for node in data["resolve"]["nodes"]}
    for owner in (roots[0], runtime[0]):
        require(package["id"] in nodes[owner["id"]]["dependencies"],
                "root and Tauri runtime must share Wry ID")


def verify_output(output):
    """Verify previously prepared inputs after a build; never run Cargo."""
    output = no_symlink(output)
    provenance = json.loads(read_regular(output / "provenance.json"))
    inventory = json.loads(read_regular(output / "prepared-files.json"))
    require(set(inventory) == {"app", "wry"}, "unexpected inventory")
    trees = {}
    for prefix, expected in inventory.items():
        actual = {}
        for name, checksum in expected.items():
            safe_name(name)
            data = read_regular(output / prefix / name)
            require(digest(data) == checksum, "prepared input changed")
            actual[name] = data
        for directory, dirs, names in os.walk(str(output / prefix), followlinks=False):
            for name in dirs + names:
                path = no_symlink(Path(directory) / name)
                relative = path.relative_to(output / prefix).as_posix()
                if path.is_file() and relative not in expected:
                    # Tauri may create JSON schemas; no extra executable/source input.
                    require(prefix == "app" and relative.startswith("src-tauri/gen/")
                            and path.suffix == ".json", "unexpected prepared file")
        trees[prefix] = actual
    require(tree_digest(trees["app"]) == provenance["preparedSourceSha256"],
            "prepared app provenance mismatch")
    require(tree_digest(trees["wry"]) == provenance["wrySourceSha256"],
            "prepared Wry provenance mismatch")
    require(digest(trees["app"]["src-tauri/Cargo.lock"]) ==
            provenance["generatedLockSha256"], "prepared lock provenance mismatch")
    require(provenance["variant"] == "wry-startup-trace-1"
            and provenance["wryVersion"] == VERSION
            and provenance["archiveSha256"] == ARCHIVE_SHA256
            and provenance["patchSha256"] == BUNDLE_SHA256, "unexpected provenance")
    return provenance


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-root", type=Path)
    parser.add_argument("--archive", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--verify-output", action="store_true")
    parser.add_argument("--metadata", type=Path)
    args = parser.parse_args()
    try:
        if args.verify_output:
            require(args.source_root is None and args.archive is None,
                    "verification accepts only --output")
            verify_output(args.output)
            if args.metadata is not None:
                verify_graph(json.loads(read_regular(args.metadata)), args.output)
        else:
            require(args.metadata is None, "--metadata requires --verify-output")
            require(args.source_root is not None and args.archive is not None,
                    "--source-root and --archive required")
            prepare(args.source_root, args.archive, args.output)
    except Exception:
        # CI logs must not include input paths, decoder payloads, or raw errors.
        parser.exit(1, "WRY_PREPARATION_REJECTED\n")
    print("Wry diagnostic inputs verified." if args.verify_output else
          "Prepared isolated Wry diagnostic input; no build or dependency resolution performed.")


if __name__ == "__main__":
    main()
