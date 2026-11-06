"""Exact Wry 0.57.1 diagnostic overlay. Never modifies the Cargo registry cache."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import tarfile

CRATE_SHA = "759c50ddad0ef9913bb5496cb9d509a50433a3b78cb0238d61105526589cf0e9"
FILE = "src/webview2/mod.rs"
LOCK_SHA = "a7d19325d5c922243009f39f6a7f9da225abf8a497b280ff8bf49081f2597af6"


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise ValueError("WRY_PROBE_ARGUMENT_REJECTED")


def replace_once(text: str, old: str, new: str) -> str:
    if text.count(old) != 1:
        raise ValueError("WRY_PROBE_SOURCE_REJECTED")
    return text.replace(old, new, 1)


def patch(text: str) -> str:
    # No new request, posted message, wait, timeout or error conversion. Original
    # callback send and nested Result are evaluated once and returned unchanged.
    for name, first in (("create_environment", 1), ("create_controller", 5)):
        begin = text.index(f"  fn {name}(")
        end = text.index("\n  #[", begin)
        section = text[begin:end]
        brace = section.index(" {\n") + len(" {\n")
        section = section[:brace] + f"    unsafe {{ rescue_host_wry_stage({first}); }}\n" + section[brace:]
        indent = "            " if first == 1 else "        "
        old = (f"{indent}tx.send(result)\n"
               f"{indent}  .map_err(|_| windows::core::Error::from(E_UNEXPECTED))")
        new = (f"{indent}let sent = tx.send(result)\n"
               f"{indent}  .map_err(|_| windows::core::Error::from(E_UNEXPECTED));\n"
               f"{indent}if sent.is_ok() {{ unsafe {{ rescue_host_wry_stage({first + 2}); }} }}\n"
               f"{indent}sent")
        section = replace_once(section, old, new)
        section = replace_once(section, "    webview2_com::wait_with_pump(rx)?",
            f"    unsafe {{ rescue_host_wry_stage({first + 1}); }}\n"
            "    let result = webview2_com::wait_with_pump(rx)?;\n"
            f"    unsafe {{ rescue_host_wry_stage({first + 3}); }}\n"
            "    result")
        text = text[:begin] + section + text[end:]
    return ('// Diagnostic-only symbol supplied by the explicit Host startup feature.\n'
            'unsafe extern "C" { fn rescue_host_wry_stage(code: u8); }\n' + text)


def materialize(archive: Path, output: Path) -> dict[str, object]:
    if archive.is_symlink() or not archive.is_file() or output.exists():
        raise ValueError("WRY_PROBE_INPUT_REJECTED")
    if hashlib.sha256(archive.read_bytes()).hexdigest() != CRATE_SHA:
        raise ValueError("WRY_PROBE_CRATE_REJECTED")
    with tarfile.open(archive, "r:gz") as tar:
        members = tar.getmembers()
        if len(members) > 512 or sum(m.size for m in members) > 8 * 1024 * 1024:
            raise ValueError("WRY_PROBE_ARCHIVE_REJECTED")
        files: dict[str, bytes] = {}
        for member in members:
            parts = member.name.split("/")
            if parts[0] != "wry-0.57.1" or any(p in ("", ".", "..") for p in parts):
                raise ValueError("WRY_PROBE_ARCHIVE_REJECTED")
            if member.isdir():
                continue
            name = "/".join(parts[1:])
            if not member.isfile() or name in files:
                raise ValueError("WRY_PROBE_ARCHIVE_REJECTED")
            stream = tar.extractfile(member)
            if stream is None:
                raise ValueError("WRY_PROBE_ARCHIVE_REJECTED")
            files[name] = stream.read()
    original = files[FILE]
    patched = patch(original.decode("utf-8")).encode("utf-8")
    output.mkdir()
    for name, data in files.items():
        target = output / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(patched if name == FILE else data)
    return {"schema": 1, "variant": "wry-completion-overlay-1", "crateSha256": CRATE_SHA,
            "originalSha256": hashlib.sha256(original).hexdigest(),
            "patchedSha256": hashlib.sha256(patched).hexdigest(), "files": len(files)}


def project_lock(data: bytes) -> bytes:
    # Only the two known Git working-file representations, never arbitrary newline normalization.
    if b"\r" in data:
        canonical = data.replace(b"\r\n", b"\n")
        if b"\r" in canonical or canonical.replace(b"\n", b"\r\n") != data:
            raise ValueError("WRY_PROBE_LOCK_REJECTED")
    else:
        canonical = data
    if hashlib.sha256(canonical).hexdigest() != LOCK_SHA:
        raise ValueError("WRY_PROBE_LOCK_REJECTED")
    old = ('name = "wry"\nversion = "0.57.1"\n'
           'source = "registry+https://github.com/rust-lang/crates.io-index"\n'
           f'checksum = "{CRATE_SHA}"\n').encode()
    if canonical.count(old) != 1:
        raise ValueError("WRY_PROBE_LOCK_REJECTED")
    return canonical.replace(old, b'name = "wry"\nversion = "0.57.1"\n', 1)


def main() -> int:
    parser = Parser(add_help=False)
    parser.add_argument("--archive", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--project", type=Path)
    try:
        args = parser.parse_args()
        result = materialize(args.archive, args.output)
        if args.project:
            lock = args.project / "ui/tauri/src-tauri/Cargo.lock"
            config = args.output.parent / "wry-probe.toml"
            if lock.is_symlink() or config.exists():
                raise ValueError("WRY_PROBE_LOCK_REJECTED")
            data = project_lock(lock.read_bytes())
            lock.write_bytes(data)
            config.write_text('[patch.crates-io]\nwry = { path = '
                              + json.dumps(args.output.resolve().as_posix()) + ' }\n', encoding="utf-8")
            result["originalLockSha256"] = LOCK_SHA
            result["overlayLockSha256"] = hashlib.sha256(data).hexdigest()
    except (OSError, ValueError, tarfile.TarError, UnicodeError):
        print('{"code":"WRY_PROBE_PREPARATION_REJECTED"}')
        return 1
    print(json.dumps(result, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
