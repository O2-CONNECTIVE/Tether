"""Build input verifier for the isolated Windows-only pinned tauri variant."""
import argparse
import importlib.util
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('pinned_patch', ROOT / 'scripts/apply_driver_trace.py')
pinned = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pinned)
CODES = ['owned_attach.rs', 'owner_native.rs', 'owner_app.rs', 'owner_listeners.rs']
pinned.ALLOWED = {'src/main.rs', 'src/server.rs'} | {'src/' + name for name in CODES}


def prepare(archive, source, manifest, binary=None):
    bundle = Path(__file__).resolve().parent
    before = pinned.upstream_files(archive.read_bytes())
    patch = (bundle / 'driver.patch').read_bytes()
    after = pinned.apply_exact(before, patch)
    for name in CODES:
        after['src/' + name] = (bundle / name).read_bytes()
    if binary is None:
        source.mkdir()  # Never overlay an old build or retry a previous invocation.
        for name, data in after.items():
            target = source / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
    pinned.check_tree(source, after)
    report = {'variant': 'tauri-driver-2.1.0-owned-attach-1',
              'upstreamSha256': pinned.UPSTREAM,
              'patchSha256': pinned.sha(patch),
              'sourceSha256': {name: pinned.sha(data) for name, data in sorted(after.items())}}
    if binary is not None:
        pinned.need(binary.is_file() and not binary.is_symlink())
        report['binarySha256'] = pinned.sha(binary.read_bytes())
    manifest.write_text(json.dumps(report, sort_keys=True), encoding='utf-8')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['archive', 'source', 'manifest']:
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--binary', type=Path)
    try:
        args = parser.parse_args()
        prepare(args.archive, args.source, args.manifest, args.binary)
    except Exception:
        print('OWNED_ATTACH_SOURCE_REJECTED')
        return 1
    print('OWNED_ATTACH_SOURCE_VERIFIED')
    return 0


if __name__ == '__main__':
    sys.exit(main())
