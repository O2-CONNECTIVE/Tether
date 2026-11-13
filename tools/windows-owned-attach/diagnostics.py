"""Extract only fixed compiler codes and approved relative source locations."""
import argparse
import json
from pathlib import Path
import re

FILES = {'main.rs', 'server.rs', 'owned_attach.rs', 'owner_native.rs', 'owner_app.rs',
         'owner_listeners.rs', 'Launcher.cs', 'LauncherNative.cs', 'Program.cs',
         'Native.cs', 'DescendantNative.cs'}
PHASES = {'preimage', 'rust-compile', 'postimage', 'compiler-present', 'launcher-compile', 'input-check', 'complete'}


def location(name, line):
    name = name.replace('\\', '/').split('/')[-1]
    if name in FILES and type(line) is int and 1 <= line <= 100000:
        return {'file': name, 'line': line}
    return {}


def extract(text):
    output = []
    for line in text.splitlines():
        try:
            value = json.loads(line)
        except ValueError:
            value = None
        if isinstance(value, dict) and value.get('reason') == 'compiler-message':
            message = value.get('message', {})
            if not isinstance(message, dict):
                continue
            if message.get('level') != 'error':
                continue
            code = (message.get('code') or {}).get('code')
            row = {'compiler': 'rust', 'code': code if isinstance(code, str) and re.fullmatch(r'E\d{4}', code) else 'unclassified-error'}
            for span in message.get('spans', []):
                if span.get('is_primary'):
                    row.update(location(span.get('file_name', ''), span.get('line_start')))
                    break
            output.append(row)
        match = re.fullmatch(r'(.*?)\((\d+)(?:,\d+)?\): error (CS\d{4}):.*', line)
        if match:
            output.append({'compiler': 'csharp', 'code': match[3], **location(match[1], int(match[2]))})
    return output[:32]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--phase', choices=sorted(PHASES), required=True)
    args = parser.parse_args()
    diagnostics = []
    for name in ['build.private.log', 'compiler.private.log']:
        path = args.directory / name
        if path.is_file() and path.stat().st_size <= 16 * 1024 * 1024:
            diagnostics.extend(extract(path.read_text(encoding='utf-8-sig', errors='replace')))
    result = {'schema': 1, 'phase': args.phase, 'passed': args.phase == 'complete', 'diagnostics': diagnostics[:32]}
    (args.directory / 'build-result.json').write_text(json.dumps(result), encoding='utf-8')


if __name__ == '__main__':
    main()
