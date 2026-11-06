"""Exact Git-backed LF/CRLF validation and private diagnostic overlays."""
import hashlib
from pathlib import Path
import subprocess
import pins


def need(value):
    if not value:
        raise ValueError('FORWARD_SOURCE_REJECTED')


def sha(data):
    return hashlib.sha256(data).hexdigest()


def git(root, *args, binary=False):
    data = subprocess.check_output(['git', '-C', str(root), *args], stderr=subprocess.DEVNULL, timeout=30)
    return data if binary else data.decode().strip()


def checkout(root, head, tree=None, product=False):
    need(Path(git(root, 'rev-parse', '--show-toplevel')).resolve() == root.resolve())
    need(git(root, 'rev-parse', 'HEAD') == head)
    if tree:
        need(git(root, 'rev-parse', 'HEAD^{tree}') == tree)
    need(not git(root, 'status', '--porcelain', '--untracked-files=no'))
    unknown = git(root, 'ls-files', '--others', '--exclude-standard').splitlines()
    need(not unknown or product and all(n.startswith(('.control/', '.diagnostic/')) for n in unknown))


def blob(root, name):
    canonical = git(root, 'show', 'HEAD:' + name, binary=True)
    file = root / name
    need(file.is_file() and not file.is_symlink())
    actual = file.read_bytes()
    need(actual == canonical or b'\r' not in canonical and actual == canonical.replace(b'\n', b'\r\n'))
    return canonical, actual


def setup_projection(data):
    text = data.decode()
    ending = '\r\n' if b'\r\n' in data else '\n'
    text = text.replace('\r\n', '\n')
    anchor = "if ($LASTEXITCODE -ne 0) { throw 'NATIVE_DIAGNOSTIC_PATCH_REJECTED' }\n"
    need(text.count(anchor) == 1)
    call = '& python "$env:FORWARD_CONTROL/diagnostics/session-forward-once/driver.py" '
    args = '--root "$env:FORWARD_ROOT" --source "$ToolsDirectory/tauri-driver-$($tools.tauriDriverVersion)" --archive $crate'
    text = text.replace(anchor, anchor + call + 'apply ' + args + '\n' +
                        "if ($LASTEXITCODE -ne 0) { throw 'NATIVE_CONNECTION_PREFLIGHT_REJECTED' }\n")
    old = '& python "$root/scripts/apply_driver_trace.py" --archive $crate --source "$ToolsDirectory/tauri-driver-$($tools.tauriDriverVersion)" --patch-folder "$root/tools/native-driver-trace" --provenance "$ToolsDirectory/driver-provenance.json" --verify-patched --binary "$ToolsDirectory/tauri/bin/tauri-driver.exe"'
    need(text.count(old) == 1)
    text = text.replace(old, call + 'verify ' + args + ' --binary "$ToolsDirectory/tauri/bin/tauri-driver.exe"')
    return text.replace('\n', ending).encode()


def supervisor_projection(data):
    old = b"$stageProvenance.variant -ne 'tauri-driver-2.1.0-stage-1'"
    need(data.count(old) == 1)
    return data.replace(old, b"$stageProvenance.variant -ne 'tauri-driver-2.1.0-connection-probe-1'")


def expected(source, diagnostic):
    files = {}
    names = git(source, 'ls-tree', '-r', '--name-only', 'HEAD', 'ui/tauri/native-smoke').splitlines()
    need(1 <= len(names) <= 80)
    for name in names:
        _, actual = blob(source, name)
        files[name] = actual
    files['ui/tauri/native-smoke/setup.ps1'] = setup_projection(files['ui/tauri/native-smoke/setup.ps1'])
    files['ui/tauri/native-smoke/supervise.ps1'] = supervisor_projection(files['ui/tauri/native-smoke/supervise.ps1'])
    names = ['scripts/apply_driver_trace.py'] + git(source, 'ls-tree', '-r', '--name-only', 'HEAD', 'tools/native-driver-trace').splitlines()
    for name in names:
        canonical, _ = blob(source, name)
        files[name] = canonical
    scope = {}
    names = git(diagnostic, 'ls-tree', '-r', '--name-only', 'HEAD', 'diagnostics/session-forward').splitlines()
    need(len(names) == 10)
    for name in names:
        canonical, _ = blob(diagnostic, name)
        scope[name] = canonical
    for name in ['scripts/apply_driver_trace.py', *git(source, 'ls-tree', '-r', '--name-only', 'HEAD', 'tools/native-driver-trace').splitlines()]:
        scope[name] = blob(source, name)[0]
    return files, scope


def put(root, files):
    need(not root.exists())
    root.mkdir(parents=True)
    for name, data in files.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)


def verify(root, files):
    paths = list(root.rglob('*'))
    need(all(not p.is_symlink() and (p.is_file() or p.is_dir()) for p in paths))
    need({p.relative_to(root).as_posix() for p in paths if p.is_file()} == set(files))
    need(all((root / n).read_bytes() == b for n, b in files.items()))


def materialize(source, diagnostic, root, create=False):
    checkout(source, pins.PRODUCT, pins.PRODUCT_TREE, True)
    checkout(diagnostic, pins.DIAGNOSTIC, pins.DIAGNOSTIC_TREE)
    files, scope = expected(source, diagnostic)
    if create:
        put(root / 'overlay', files)
        put(root / 'diagnostic-scope', scope)
    verify(root / 'overlay', files)
    verify(root / 'diagnostic-scope', scope)
    return sha(b''.join(name.encode() + b'\0' + hashlib.sha256(data).digest() for name, data in sorted(files.items()))), len(files)
