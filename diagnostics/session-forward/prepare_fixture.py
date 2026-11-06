"""Source-only projection of the pinned real handler into a pure transport test library."""
import argparse
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
import apply_driver_trace as original  # noqa: E402


def prepare(archive, output, baseline=False):
    original.need(not output.exists())
    before = original.upstream_files(archive.read_bytes())
    patch, code, expected = original.bundle(ROOT / 'tools/native-driver-trace')
    after = original.apply_exact(before, patch)
    after.update({'src/' + name: data for name, data in code.items()})
    original.need(all(original.sha(after[n]) == h for n, h in expected.items()))
    folder = Path(__file__).parent
    if not baseline:
        after = original.apply_exact(after, (folder / 'connection.patch').read_bytes())
        expected = json.loads((folder / 'postimage.json').read_text())
        original.need(all(original.sha(after[n]) == h for n, h in expected.items()))
    source_hashes = {n: original.sha(after[n]) for n in ('src/server.rs', 'src/diagnostic.rs', 'Cargo.lock')}
    # Fixture-only Mac projection: compile the EXACT Windows capability method/field.
    # No alternative mapping implementation; no app/native process is started.
    server = after['src/server.rs'].decode()
    original.need(server.count('#[cfg(target_os = "windows")]') == 2)
    server = server.replace('#[cfg(target_os = "windows")]', '#[cfg(any(target_os = "windows", test))]')
    server += '\n#[cfg(test)]\n#[path = "../forwarding_tests.rs"]\nmod forwarding_tests;\n'
    after['src/server.rs'] = server.encode()
    after['src/lib.rs'] = b'mod cli;\nmod diagnostic;\nmod server;\n'
    manifest = after['Cargo.toml'].decode()
    old = '[[bin]]\nname = "tauri-driver"\npath = "src/main.rs"'
    original.need(manifest.count(old) == 1)
    manifest = manifest.replace(old, '[lib]\nname = "transport_fixture"\npath = "src/lib.rs"')
    after['Cargo.toml'] = manifest.encode()
    after['forwarding_tests.rs'] = (folder / 'forwarding_tests.rs').read_bytes()
    output.mkdir(parents=True)
    for name, data in after.items():
        path = output / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
    (output / 'projection.json').write_text(json.dumps({'baseline': baseline, 'sourceSha256': source_hashes,
        'fixtureOnlyChanges': ['Windows cfg enabled under test', 'test module appended',
                               'library target, no executable'],
        'cargoLockUnchanged': original.sha(after['Cargo.lock']) == original.sha(before['Cargo.lock'])}, indent=2) + '\n')


def main():
    class Parser(argparse.ArgumentParser):
        def error(self, message):
            raise original.PatchError('FORWARD_FIXTURE_ARGUMENT_REJECTED')
    parser = Parser(description=__doc__)
    parser.add_argument('--archive', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--baseline', action='store_true')
    try:
        args = parser.parse_args()
        prepare(args.archive, args.output, args.baseline)
    except Exception:
        print('FORWARD_FIXTURE_SOURCE_REJECTED')
        return 1
    print('FORWARD_FIXTURE_SOURCE_PREPARED')
    return 0


if __name__ == '__main__':
    sys.exit(main())
