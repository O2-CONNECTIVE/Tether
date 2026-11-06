"""Replay actual authenticated consume with compiler-only composition fixtures."""
from collections import Counter
import json
import os
from pathlib import Path
import platform
import shutil
import sys
import tempfile

from check_mac_enrollment_lookup import command

ROOT = Path(__file__).resolve().parents[1]
PACKAGE = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport'
TEST = 'TestStandbyConsumeEndsOriginalSource'
ITERATIONS = 20


def validate_output(output):
    events = [json.loads(line) for line in output.splitlines() if line.strip()]
    if any(e.get('Action') in ('fail', 'skip') for e in events):
        raise ValueError('consume replay failed or skipped')
    passes = Counter(e['Test'] for e in events if e.get('Action') == 'pass' and 'Test' in e)
    expected = Counter({name: ITERATIONS for name in (TEST, TEST + '/close', TEST + '/policy-drift')})
    packages = [e for e in events if e.get('Action') == 'pass' and 'Test' not in e]
    if passes != expected or len(packages) != 1 or packages[0].get('Package') != PACKAGE:
        raise ValueError('consume replay incomplete or wrong tests')
    return sum(passes.values())


def overlay_files():
    replacements = {
        'residentmac/standby_consume_replay.go': 'residentmac/testdata/standby_consume_overlay.go',
        'agenttransport/standby_consume_replay_export_test.go': 'agenttransport/testdata/standby_consume_export_test.go',
        'agenttransport/standby_consume_replay_test.go': 'agenttransport/testdata/standby_consume_probe_test.go',
    }
    base = ROOT / 'backend/internal'
    if any((base / name).exists() for name in replacements):
        raise ValueError('test-only overlay escaped into normal source')
    return {'Replace': {str(base / target): str(base / source) for target, source in replacements.items()}}


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported consume replay invocation')
    arch = {'arm64': 'arm64', 'x86_64': 'amd64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported consume architecture')
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'TMPDIR', 'DEVELOPER_DIR', 'GOMODCACHE')}
    env.update(GOTOOLCHAIN='local', GOENV='off', GOWORK='off', GOPROXY='off',
               GOSUMDB='off', GOFLAGS='', GOOS='darwin', GOARCH=arch, CGO_ENABLED='1')
    go = shutil.which('go', path=env.get('PATH'))
    if go is None or command([go, 'env', 'GOVERSION'], env).strip() != 'go1.27.3':
        raise ValueError('existing reviewed Go unavailable')
    reports = []
    with tempfile.TemporaryDirectory(prefix='standby-consume-replay-') as temporary:
        directory = Path(temporary)
        overlay = directory / 'overlay.json'
        overlay.write_text(json.dumps(overlay_files()))
        env['GOCACHE'] = str(directory / 'cache')
        for race in (False, True):
            args = [go, 'test', '-mod=readonly', '-p=1', '-count=' + str(ITERATIONS),
                    '-timeout=30s', '-json', '-overlay', str(overlay), '-run', '^' + TEST + '$']
            if race:
                args += ['-race']
            output = command(args + ['./internal/agenttransport'], env, ROOT / 'backend')
            reports.append({'race': race, 'passed': validate_output(output)})
    print(json.dumps({'fixture': 'standby-consume-source', 'reports': reports,
                      'cases': 2, 'iterations': ITERATIONS, 'producerLaunches': 0,
                      'scope': 'real memory handshake and same consume/lease; no native work or live GUI proof'}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError):
        print('STANDBY_CONSUME_SOURCE_FAILED', file=sys.stderr)
        raise SystemExit(1)
