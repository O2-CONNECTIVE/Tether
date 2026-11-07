"""Two exact diagnostic path patches; never touches the Cargo registry cache."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import tarfile

import prepare_wry_probe as base

COM_SHA = "3f89fca7a704cee10dcb3654c1dbb8941d1783132f1917358af75bec37a7d7e6"
COM_ORIGINAL = "de9ec5585a990725576de47557f93529cddbdf267ab3847835689e2a01c854fe"
WRY_POST = "150fa0120f6a461875915d86675eb61dac596c992ec639a9e3424be36f1363b9"
COM_POST = "4719232d58bd642437a58b120f023748cf1ed8d339541659cc33b531bf8ebcd2"
PROTOCOL_SHA = "03c2170668ffc99c8f326d8f08b173e9d8b5e617fb53830ca8c83c14d246305d"
COM_FILES = 12


def wry_pairs(first):
    indent = "            " if first == 1 else "        "
    pairs = [
        (f"    unsafe {{ rescue_host_wry_stage({first}); }}\n",
         f"    unsafe {{ rescue_host_wry_stage({first}); }}\n"
         f"    let __delivery = webview2_com::rescue_host_delivery::Scope::begin({1 if first == 1 else 2});\n"
         "    let __delivery_token = __delivery.token();\n"),
        (("          move |error_code, environment| {\n" if first == 1 else
          "      move |error_code, controller| {\n"),
         ("          move |error_code, environment| {\n" if first == 1 else
          "      move |error_code, controller| {\n") + indent +
         "let _delivery_completion = webview2_com::rescue_host_delivery::Completion::enter(__delivery_token);\n"),
        (f"{indent}if sent.is_ok() {{ unsafe {{ rescue_host_wry_stage({first + 2}); }} }}\n",
         f"{indent}if sent.is_ok() {{ unsafe {{ rescue_host_wry_stage({first + 2}); }} }}\n"
         f"{indent}webview2_com::rescue_host_delivery::Completion::sent(__delivery_token, sent.is_ok());\n"),
        ("    let result = webview2_com::wait_with_pump(rx)?;",
         "    let result = __delivery.wait(rx)?;"),
    ]
    if first == 1:
        pairs.append(("      CreateCoreWebView2EnvironmentWithOptions(\n",
                      "      let _delivery_api = __delivery.api();\n      CreateCoreWebView2EnvironmentWithOptions(\n"))
    else:
        pairs.extend([
            ("          env10.CreateCoreWebView2ControllerWithOptions(hwnd, &controller_opts, &handler)?;",
             "          let _delivery_api = __delivery.api();\n"
             "          env10.CreateCoreWebView2ControllerWithOptions(hwnd, &controller_opts, &handler)?;"),
            ("        _ => env.CreateCoreWebView2Controller(hwnd, &handler)?,",
             "        _ => {\n          let _delivery_api = __delivery.api();\n"
             "          env.CreateCoreWebView2Controller(hwnd, &handler)?;\n        },"),
        ])
    return pairs


def patch_wry(text, inverse=False):
    for name, first in (("create_environment", 1), ("create_controller", 5)):
        begin = text.index(f"  fn {name}(")
        end = text.index("\n  #[", begin)
        section = text[begin:end]
        pairs = wry_pairs(first)
        for old, new in (reversed(pairs) if inverse else pairs):
            section = base.replace_once(section, new if inverse else old, old if inverse else new)
        text = text[:begin] + section + text[end:]
    return text


COM_PREFIX = '#[path = "rescue_host_delivery.rs"]\npub mod rescue_host_delivery;\n'
COM_PAIRS = [
    ("if let Ok(result) = rx.try_recv() {",
     "if let Ok(result) = rescue_host_delivery::Pump::observe(rx.try_recv()) {"),
    ("    let mut msg = MSG::default();\n", "    let mut msg = MSG::default();\n"
     "    let _delivery_pump = rescue_host_delivery::Pump::enter();\n"),
    ("            match WindowsAndMessaging::GetMessageA(&mut msg, None, 0, 0).0 {",
     "            rescue_host_delivery::Pump::phase(2);\n"
     "            match rescue_host_delivery::Pump::get_return(WindowsAndMessaging::GetMessageA(&mut msg, None, 0, 0).0) {"),
    ("                    let _ = WindowsAndMessaging::TranslateMessage(&msg);\n"
     "                    WindowsAndMessaging::DispatchMessageA(&msg);",
     "                    rescue_host_delivery::Pump::phase(0);\n"
     "                    let _ = WindowsAndMessaging::TranslateMessage(&msg);\n"
     "                    rescue_host_delivery::Pump::phase(3);\n"
     "                    WindowsAndMessaging::DispatchMessageA(&msg);\n"
     "                    rescue_host_delivery::Pump::dispatch_return();"),
]


def patch_com(text, inverse=False):
    if inverse:
        if not text.startswith(COM_PREFIX): raise ValueError("DELIVERY_SOURCE_REJECTED")
        text = text[len(COM_PREFIX):]
    for old, new in (reversed(COM_PAIRS) if inverse else COM_PAIRS):
        text = base.replace_once(text, new if inverse else old, old if inverse else new)
    return text if inverse else COM_PREFIX + text


def protocol_bytes():
    data = (Path(__file__).parents[1] / "tools/wry-delivery/protocol.rs").read_bytes()
    canonical = data.replace(b"\r\n", b"\n")
    if b"\r" in canonical or data not in (canonical, canonical.replace(b"\n", b"\r\n")):
        raise ValueError("DELIVERY_SOURCE_REJECTED")
    if hashlib.sha256(canonical).hexdigest() != PROTOCOL_SHA:
        raise ValueError("DELIVERY_SOURCE_REJECTED")
    return canonical


def com_files(archive):
    if archive.is_symlink() or not archive.is_file() or hashlib.sha256(archive.read_bytes()).hexdigest() != COM_SHA:
        raise ValueError("DELIVERY_CRATE_REJECTED")
    result = {}
    with tarfile.open(archive, "r:gz") as bundle:
        members = bundle.getmembers()
        if len(members) > 256 or sum(x.size for x in members) > 2 * 1024 * 1024:
            raise ValueError("DELIVERY_CRATE_REJECTED")
        for item in members:
            parts = item.name.split("/")
            if parts[0] != "webview2-com-0.39.1" or any(p in ("", ".", "..") for p in parts):
                raise ValueError("DELIVERY_CRATE_REJECTED")
            if item.isdir(): continue
            name = "/".join(parts[1:])
            if not item.isfile() or name in result: raise ValueError("DELIVERY_CRATE_REJECTED")
            result[name] = bundle.extractfile(item).read()
    return result


def project_lock(data):
    canonical = base.project_lock(data)
    old = ('name = "webview2-com"\nversion = "0.39.1"\n'
           'source = "registry+https://github.com/rust-lang/crates.io-index"\n'
           f'checksum = "{COM_SHA}"\n').encode()
    if canonical.count(old) != 1: raise ValueError("DELIVERY_LOCK_REJECTED")
    return canonical.replace(old, b'name = "webview2-com"\nversion = "0.39.1"\n', 1)


def config_bytes(wry, completion):
    return ('[patch.crates-io]\nwry = { path = ' + json.dumps(wry.resolve().as_posix()) + ' }\n'
            'webview2-com = { path = ' + json.dumps(completion.resolve().as_posix()) + ' }\n').encode()


def materialize(archive, output, completion_archive, completion_output):
    if completion_output.exists() or completion_output.parent != output.parent:
        raise ValueError("DELIVERY_OUTPUT_REJECTED")
    files = com_files(completion_archive)
    original = files["src/lib.rs"]
    if hashlib.sha256(original).hexdigest() != COM_ORIGINAL: raise ValueError("DELIVERY_SOURCE_REJECTED")
    patched = patch_com(original.decode()).encode()
    if patch_com(patched.decode(), True).encode() != original: raise ValueError("DELIVERY_SOURCE_REJECTED")
    protocol = protocol_bytes()
    meta = base.materialize(archive, output)
    wry_file = output / base.FILE
    before = wry_file.read_bytes()
    after = patch_wry(before.decode()).encode()
    if patch_wry(after.decode(), True).encode() != before: raise ValueError("DELIVERY_SOURCE_REJECTED")
    if hashlib.sha256(after).hexdigest() != WRY_POST or hashlib.sha256(patched).hexdigest() != COM_POST:
        raise ValueError("DELIVERY_SOURCE_REJECTED")
    wry_file.write_bytes(after)
    files["src/lib.rs"] = patched
    files["src/rescue_host_delivery.rs"] = protocol
    if len(files) != COM_FILES: raise ValueError("DELIVERY_SOURCE_REJECTED")
    completion_output.mkdir()
    for name, data in files.items():
        target = completion_output / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    meta.update(variant="wry-sdk-delivery-overlay-1", patchedSha256=WRY_POST,
                completionCrateSha256=COM_SHA, completionOriginalSha256=COM_ORIGINAL,
                completionPatchedSha256=COM_POST, completionFiles=COM_FILES, protocolSha256=PROTOCOL_SHA)
    return meta


def main():
    parser = base.Parser(add_help=False)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--completion-archive", type=Path, required=True)
    parser.add_argument("--completion-output", type=Path, required=True)
    parser.add_argument("--project", type=Path, required=True)
    try:
        args = parser.parse_args()
        lock = args.project / "ui/tauri/src-tauri/Cargo.lock"
        config = args.output.parent / "wry-probe.toml"
        if lock.is_symlink() or config.exists(): raise ValueError("DELIVERY_LOCK_REJECTED")
        projected = project_lock(lock.read_bytes())
        meta = materialize(args.archive, args.output, args.completion_archive, args.completion_output)
        lock.write_bytes(projected)
        config.write_bytes(config_bytes(args.output, args.completion_output))
        meta.update(originalLockSha256=base.LOCK_SHA, overlayLockSha256=hashlib.sha256(projected).hexdigest())
    except (OSError, ValueError, tarfile.TarError, UnicodeError):
        print('{"code":"WRY_DELIVERY_PREPARATION_REJECTED"}')
        return 1
    print(json.dumps(meta, separators=(",", ":")))
    return 0


if __name__ == "__main__": raise SystemExit(main())
