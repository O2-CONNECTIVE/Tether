"""One attempt controller: fixed sources, private logs, original native authority."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import driver
import legacy
import overlay
import pins
import probe_schema

HERE = Path(__file__).resolve().parent
STAGES = ('environment', 'preflight', 'tools', 'frontend', 'build', 'trial')
COMMANDS = ('original-process-contract', 'inventory', 'control-python', 'original-python',
    'generated-python', 'original-node', 'powershell-parser', 'tools', 'npm-ci', 'frontend', 'product-build', 'native', 'audit')
NODE_SUMMARY = r'# tests 15[\s\S]*# pass 15[\s\S]*# fail 0[\s\S]*# cancelled 0[\s\S]*# skipped 0[\s\S]*# todo 0'
NATIVE_FILES = ('native-result.json', 'native-observation.json', 'session-startup-timing.json', 'webview-result.json')


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    pending = path.with_name(path.name + '.pending')
    # Same-directory replace preserves the last complete checkpoint on interruption.
    # A stale pending file is uncertainty, not permission to overwrite/re-enter.
    with pending.open('x', encoding='utf-8') as stream:
        stream.write(json.dumps(value, sort_keys=True, indent=2) + '\n')
    os.replace(pending, path)


def read(path):
    return legacy.read(path)


def validate_rejections(value):
    overlay.need(type(value) is dict and set(value) == {'schema', 'stage', 'finalIdentity'})
    overlay.need(type(value['schema']) is int and value['schema'] == 1)
    overlay.need(all(value[k] is None or type(value[k]) is str and value[k] in overlay.SITES
                     for k in ('stage', 'finalIdentity')))
    return value


def read_rejections(root):
    path = root / 'safe/rejection-sites.json'
    overlay.need(path.stat().st_size <= 512)
    return validate_rejections(read(path))


def record_rejection(root, key, error, fallback):
    # First failure per boundary wins. No exception text/type/value is serialized.
    overlay.need(key in {'stage', 'finalIdentity'} and fallback in overlay.SITES)
    path = root / 'safe/rejection-sites.json'
    value = read_rejections(root) if path.exists() else {'schema': 1, 'stage': None, 'finalIdentity': None}
    if value[key] is None:
        value[key] = error.site if isinstance(error, overlay.Rejection) else fallback
        write(path, validate_rejections(value))


def rejection_facts(root, state, stable):
    failed = any(s == 'failed' for s in state['stages'].values())
    unknown = {'stage': 'unknown' if failed else None, 'finalIdentity': 'unknown' if not stable else None}
    try:
        path = root / 'safe/rejection-sites.json'
        value = read_rejections(root) if path.exists() else {'schema': 1, 'stage': None, 'finalIdentity': None}
        overlay.need(not failed or value['stage'] is not None)
        overlay.need(stable or value['finalIdentity'] is not None)
        return {k: value[k] for k in unknown}, True
    except Exception:
        return unknown, False


def build_verified(root):
    path = root / 'safe/command-ledger.json'
    if not path.exists():
        return False
    value = validate_ledger(read(path))['commands']['product-build']
    return value == {'state': 'succeeded', 'exitCode': 0, 'outputContractVerified': True}


def validate_state(value):
    overlay.need(type(value) is dict and set(value) == {'schema', 'stages', 'nativeInvoked', 'auditInvoked'})
    overlay.need(type(value['schema']) is int and value['schema'] == 1)
    overlay.need(type(value['nativeInvoked']) is bool and type(value['auditInvoked']) is bool)
    overlay.need(type(value['stages']) is dict and set(value['stages']) == set(STAGES))
    overlay.need(all(v in {'not-run', 'running', 'passed', 'failed'} for v in value['stages'].values()))
    pending = False
    for name in STAGES:
        status = value['stages'][name]
        overlay.need(not pending or status == 'not-run')
        if status != 'passed':
            pending = True
    overlay.need(not value['auditInvoked'] or value['nativeInvoked'])
    overlay.need(not value['nativeInvoked'] or value['stages']['trial'] in {'running', 'passed', 'failed'})
    overlay.need(value['stages']['trial'] != 'passed' or value['nativeInvoked'])
    return value


def command(root, label, args, cwd=None, env=None, timeout=240):
    overlay.need(label in COMMANDS)
    file = root / 'safe/command-ledger.json'
    ledger = validate_ledger(read(file)) if file.exists() else {'schema': 1, 'commands': {
        name: {'state': 'not-run', 'exitCode': None, 'outputContractVerified': None} for name in COMMANDS}}
    current = ledger['commands'][label]
    overlay.need(current['state'] == 'not-run')
    current['state'] = 'running'
    write(file, ledger)
    try:
        with (root / 'private' / (label + '.log')).open('wb') as stream:
            result = subprocess.run(args, cwd=cwd, env=env, stdout=stream, stderr=subprocess.STDOUT,
                                    timeout=timeout, check=False)
        current['state'] = 'succeeded' if result.returncode == 0 else 'failed'
        current['exitCode'] = result.returncode if 0 <= result.returncode <= 255 else None
        write(file, ledger)
        return result.returncode
    except Exception as error:
        current['state'] = 'timeout' if isinstance(error, subprocess.TimeoutExpired) else 'uncertain'
        write(file, ledger)
        raise


def validate_ledger(value):
    overlay.need(type(value) is dict and set(value) == {'schema', 'commands'} and type(value['schema']) is int and value['schema'] == 1)
    overlay.need(type(value['commands']) is dict and set(value['commands']) == set(COMMANDS))
    for row in value['commands'].values():
        overlay.need(type(row) is dict and set(row) == {'state', 'exitCode', 'outputContractVerified'})
        overlay.need(row['state'] in {'not-run', 'running', 'succeeded', 'failed', 'timeout', 'uncertain'})
        overlay.need(row['exitCode'] is None or type(row['exitCode']) is int and 0 <= row['exitCode'] <= 255)
        overlay.need(row['outputContractVerified'] is None or type(row['outputContractVerified']) is bool)
        overlay.need(row['state'] != 'succeeded' or row['exitCode'] == 0)
        overlay.need(row['state'] not in {'not-run', 'running', 'timeout', 'uncertain'} or row['exitCode'] is None)
    return value


def checked(root, label, args, cwd=None, expected=None, timeout=240):
    success = command(root, label, args, cwd, timeout=timeout) == 0
    if success and expected:
        text = (root / 'private' / (label + '.log')).read_text(encoding='utf-8', errors='replace')
        success = re.search(expected, text) is not None
    file = root / 'safe/command-ledger.json'
    ledger = validate_ledger(read(file)); ledger['commands'][label]['outputContractVerified'] = success
    write(file, ledger)
    overlay.need(success)


def module(name, file):
    sys.path.insert(0, str(file.parent))
    spec = importlib.util.spec_from_file_location(name, file)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def boundary(source, root, create=False):
    overlay.need(source.resolve() == Path(os.environ['GITHUB_WORKSPACE']).resolve())
    overlay.need(root.resolve() == (Path(os.environ['RUNNER_TEMP']) / 'pr231-connection-once').resolve())
    control, diagnostic = source / '.control', source / '.diagnostic'
    event = read(Path(os.environ['GITHUB_EVENT_PATH']))
    pr = event.get('pull_request', {})
    overlay.need(os.environ.get('GITHUB_RUN_ATTEMPT') == '1' and event.get('action') == 'opened')
    overlay.need(pr.get('draft') is True and pr.get('head', {}).get('ref') == pins.BRANCH)
    overlay.need(pr.get('base', {}).get('ref') == pins.BASE and pr['base']['sha'] == pins.BASE_SHA)
    overlay.need(pr['head']['repo']['full_name'] == os.environ.get('GITHUB_REPOSITORY') == 'O2-CONNECTIVE/rescue-host')
    head = pr['head']['sha']
    overlay.need(re.fullmatch('[0-9a-f]{40}', head) is not None and head == os.environ.get('EXPECTED_CONTROL'))
    overlay.need(os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted' and os.environ.get('RUNNER_OS') == 'Windows' and os.environ.get('RUNNER_ARCH') == 'X64')
    overlay.checkout(control, head)
    names = overlay.git(control, 'ls-tree', '-r', '--name-only', 'HEAD', 'diagnostics/session-forward-once', '.github/workflows/pr231-connection-once.yml').splitlines()
    overlay.need(len(names) >= 8)
    for name in names:
        overlay.blob(control, name)
    generated = build_verified(root)
    overlay.checkout(source, pins.PRODUCT, pins.PRODUCT_TREE, True, generated)
    overlay.checkout(diagnostic, pins.DIAGNOSTIC, pins.DIAGNOSTIC_TREE)
    if create:
        overlay.need(not root.exists())
        (root / 'private').mkdir(parents=True)
        (root / 'safe').mkdir()
    digest, count = overlay.materialize(source, diagnostic, root, create, generated)
    return control, diagnostic, head, digest, count


def allow_native_codes(source):
    for name in overlay.git(source, 'ls-tree', '-r', '--name-only', pins.PRODUCT, 'ui/tauri/native-smoke').splitlines():
        # Publication must still retain safe old facts if working source drifted.
        text = overlay.git(source, 'show', pins.PRODUCT + ':' + name, binary=True).decode('utf-8')
        legacy.WORDS.update(re.findall(r'\bNATIVE_[A-Z_]{1,80}\b', text))


def identity(source, root, final=False):
    site = 'identity-boundary'
    try:
        control, _, head, digest, count = boundary(source, root)
        exe = source / 'ui/tauri/src-tauri/target/debug/rescue-host-shell.exe'
        binary = root / 'tools/tauri/bin/tauri-driver.exe'
        site = 'identity-hash'
        value = {'schema': 1, 'product': pins.PRODUCT, 'productTree': pins.PRODUCT_TREE,
            'diagnostic': pins.DIAGNOSTIC, 'diagnosticTree': pins.DIAGNOSTIC_TREE,
            'control': head, 'controlTree': overlay.git(control, 'rev-parse', 'HEAD^{tree}'),
            'base': pins.BASE_SHA, 'overlaySha256': digest, 'overlayFileCount': count,
            'supervisorSha256': overlay.sha((root / 'overlay/ui/tauri/native-smoke/supervise.ps1').read_bytes()),
            'setupSha256': overlay.sha((root / 'overlay/ui/tauri/native-smoke/setup.ps1').read_bytes()),
            'exeSha256': legacy.digest(exe) if exe.exists() else None,
            'driverSha256': legacy.digest(binary) if binary.exists() else None,
            'cargoLockSha256': pins.LOCK, 'instrumentationEnabled': True,
            'productFeatures': 'custom-protocol', 'productProfile': 'debug', 'nativeRequestDeadlineMs': 15000}
        site = 'identity-comparison'
        if final and (root / 'safe/launch-identity.json').exists():
            overlay.need(value == read(root / 'safe/launch-identity.json'), site)
        site = 'identity-write'
        write(root / 'safe' / ('final-identity.json' if final else 'build-identity.json'), value)
        return value
    except Exception as error:
        try:
            record_rejection(root, 'finalIdentity' if final else 'stage', error, site)
        except Exception:
            pass
        raise


def audit(source, root, state):
    overlay.need(not state['auditInvoked'])
    state['auditInvoked'] = True
    write(root / 'safe/run-status.json', state)
    exe = source / 'ui/tauri/src-tauri/target/debug/rescue-host-shell.exe'
    return command(root, 'audit', ['pwsh', '-NoLogo', '-NoProfile', '-File', str(HERE / 'audit.ps1'),
        '-ApplicationPath', str(exe), '-ToolsDirectory', str(root / 'tools'),
        '-HarnessDirectory', str(root / 'overlay/ui/tauri/native-smoke'),
        '-OutputPath', str(root / 'audit.json')], timeout=30)


def versions(root, rust=False):
    overlay.need(sys.version_info[:3] == (3, 13, 17))
    def output(args):
        value = subprocess.check_output(args, stderr=subprocess.DEVNULL, timeout=30).decode().strip()
        overlay.need(len(value) <= 128)
        return value
    overlay.need(output(['node', '--version']) == 'v24.22.1')
    powershell = output(['pwsh', '-NoLogo', '-NoProfile', '-Command', '$PSVersionTable.PSVersion.ToString()'])
    overlay.need(re.fullmatch(r'\d{1,3}(?:\.\d{1,5}){2,3}', powershell))
    if rust:
        overlay.need(re.fullmatch(r'rustc 1\.99\.1 \([0-9a-f]{7,40} \d{4}-\d{2}-\d{2}\)', output(['rustc', '+1.99.1', '--version'])))
    write(root / 'safe/runtime-versions.json', {'schema': 1, 'python': '3.13.17', 'node': '24.22.1',
        'rust': '1.99.1' if rust else None, 'powershell': powershell})


def trial(source, root, state):
    overlay.need(not state['nativeInvoked'] and not (root / 'tools/webview-profile').exists())
    driver.perform('verify', root, root / 'tools/tauri-driver-2.1.0', root / 'tools/tauri-driver.crate',
                   root / 'tools/tauri/bin/tauri-driver.exe')
    value = identity(source, root)
    overlay.need(value['exeSha256'] is not None and value['driverSha256'] is not None)
    write(root / 'safe/launch-identity.json', value)
    state['nativeInvoked'] = True
    write(root / 'safe/run-status.json', state)
    exits = {'schema': 1, 'supervisorSucceeded': None, 'inspectorSucceeded': None,
             'auditSucceeded': None, 'identityStable': None}
    write(root / 'safe/trial-exits.json', exits)
    # No wrapper kill/timeout of the original cleanup authority. The unchanged
    # supervisor limits and the workflow's original 3-minute native step apply.
    code = command(root, 'native', ['pwsh', '-NoLogo', '-NoProfile', '-File',
        str(root / 'overlay/ui/tauri/native-smoke/supervise.ps1'), '-ToolsDirectory', str(root / 'tools'),
        '-OutputDirectory', str(root / 'observation'), '-ApplicationPath',
        str(source / 'ui/tauri/src-tauri/target/debug/rescue-host-shell.exe')], timeout=None)
    exits['supervisorSucceeded'] = code == 0
    write(root / 'safe/trial-exits.json', exits)
    inspector = False
    try:
        driver.perform('verify', root, root / 'tools/tauri-driver-2.1.0', root / 'tools/tauri-driver.crate',
                       root / 'tools/tauri/bin/tauri-driver.exe')
        original = module('original_trace_verifier', source / 'scripts/verify_driver_trace.py')
        path = root / 'tools/driver-stage.jsonl'
        overlay.need(path.is_file() and not path.is_symlink() and path.stat().st_size <= 8192)
        stage, capture = probe_schema.inspect(path.read_bytes(), original.inspect)
        write(root / 'safe/driver-stage-report.json', legacy.safe(stage))
        write(root / 'safe/native-connection-record.json', probe_schema.validate_capture(capture))
        inspector = stage['diagnostic'] == 'complete' and capture['captureComplete']
    except Exception:
        write(root / 'safe/native-connection-record.json', probe_schema.unavailable())
    exits['inspectorSucceeded'] = inspector
    write(root / 'safe/trial-exits.json', exits)
    audit_code = audit(source, root, state)
    exits['auditSucceeded'] = audit_code == 0
    write(root / 'safe/trial-exits.json', exits)
    stable = True
    try:
        identity(source, root, True)
    except Exception:
        stable = False
    exits['identityStable'] = stable
    write(root / 'safe/trial-exits.json', exits)
    overlay.need(code == 0 and inspector and audit_code == 0 and stable)


def perform(stage, source, root, state):
    native = root / 'overlay/ui/tauri/native-smoke'
    if stage == 'environment':
        for key in ('CARGO_TARGET_DIR', 'RUSTFLAGS', 'NODE_OPTIONS', 'RESCUE_HOST_STARTUP_TRACE_DIR'):
            overlay.need(not os.environ.get(key))
        checked(root, 'original-process-contract', ['pwsh', '-NoLogo', '-NoProfile', '-File', str(native / 'process-contract.test.ps1')])
        checked(root, 'inventory', ['pwsh', '-NoLogo', '-NoProfile', '-File', str(native / 'inventory.ps1'), '-OutputPath', str(root / 'inventory.json')])
    elif stage == 'preflight':
        versions(root)
        checked(root, 'control-python', [sys.executable, '-B', '-m', 'unittest', 'discover', '-s', str(HERE), '-p', 'test_once.py', '-v'], expected=r'Ran 23 tests[\s\S]*\nOK\s*$')
        checked(root, 'generated-python', [sys.executable, '-B', '-m', 'unittest', 'discover', '-s', str(HERE), '-p', 'test_generated.py', '-v'], expected=r'Ran 8 tests[\s\S]*\nOK\s*$')
        checked(root, 'original-python', [sys.executable, '-B', '-m', 'unittest', 'discover', '-s', str(source / 'scripts/tests'), '-p', 'test_driver_*.py', '-v'], expected=r'Ran 10 tests[\s\S]*\nOK\s*$')
        checked(root, 'original-node', ['node', '--test', '--test-reporter=tap', str(source / 'ui/tauri/tests/native-contract.test.ts'), str(source / 'ui/tauri/tests/session-observation.test.ts')], expected=NODE_SUMMARY)
        count = len(list(native.glob('*.ps1')))
        checked(root, 'powershell-parser', ['pwsh', '-NoLogo', '-NoProfile', '-File', str(HERE / 'parse.ps1'), '-Harness', str(native)], expected=r'FORWARD_PARSE_PASS count=' + str(count) + r'\b')
        write(root / 'safe/core-preflight.json', {'schema': 1, 'controlPythonPassed': 23,
            'originalPythonPassed': 10, 'originalNodePassed': 15, 'powershellFilesParsed': count})
    elif stage == 'tools':
        versions(root, True)
        checked(root, 'tools', ['pwsh', '-NoLogo', '-NoProfile', '-File', str(native / 'setup.ps1'), '-ToolsDirectory', str(root / 'tools'), '-InventoryPath', str(root / 'inventory.json')], timeout=600)
        overlay.need((root / 'safe/transport-preflight.json').is_file())
    elif stage == 'frontend':
        cwd = source / 'ui/tauri'
        checked(root, 'npm-ci', ['npm.cmd', 'ci', '--ignore-scripts', '--no-audit', '--no-fund'], cwd=cwd)
        checked(root, 'frontend', ['npm.cmd', 'run', 'build'], cwd=cwd)
    elif stage == 'build':
        key, matched, hit = (os.environ.get(n, '') for n in ('REQUESTED_CACHE_KEY', 'CACHE_MATCHED_KEY', 'CACHE_HIT'))
        overlay.need(re.fullmatch(re.escape(pins.PREFIX) + '[0-9a-f]{64}', key) is not None)
        lookup = os.environ.get('CACHE_LOOKUP_EXACT')
        overlay.need(lookup in {'true', 'false'} and hit in {'true', 'false', ''})
        exact = hit == 'true' and matched == key
        miss = lookup == 'false' and hit in {'false', ''} and matched == ''
        write(root / 'safe/cache.json', {'schema': 1, 'keySha256Suffix': key[len(pins.PREFIX):],
            'lookupExact': lookup == 'true', 'exactHit': exact, 'restoreOnly': True,
            'state': 'exact-hit' if lookup == 'true' and exact else 'miss' if miss else 'restore-mismatch'})
        overlay.need(lookup == 'true' and exact or miss)
        checked(root, 'product-build', ['cargo', '+1.99.1', 'build', '--locked', '--manifest-path', 'src-tauri/Cargo.toml', '--features', 'custom-protocol'], cwd=source / 'ui/tauri', timeout=900)
        identity(source, root)
    elif stage == 'trial':
        trial(source, root, state)


def main():
    class Parser(argparse.ArgumentParser):
        def error(self, message):
            raise ValueError('FORWARD_ARGUMENT_REJECTED')
    parser = Parser()
    parser.add_argument('stage', choices=['guard', *STAGES, 'finalize'])
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--root', type=Path, required=True)
    state = None
    entered = False
    try:
        args = parser.parse_args()
        source, root = args.source.resolve(), args.root.resolve()
        if args.stage == 'guard':
            boundary(source, root, True)
            state = {'schema': 1, 'stages': {s: 'not-run' for s in STAGES}, 'nativeInvoked': False, 'auditInvoked': False}
            write(root / 'safe/run-status.json', state)
            identity(source, root)
        elif args.stage == 'finalize':
            from publish import finalize
            return finalize(source, root)
        else:
            boundary(source, root)
            state = validate_state(read(root / 'safe/run-status.json'))
            index = STAGES.index(args.stage)
            overlay.need(all(state['stages'][s] == 'passed' for s in STAGES[:index]))
            overlay.need(state['stages'][args.stage] == 'not-run')
            entered = True
            state['stages'][args.stage] = 'running'
            write(root / 'safe/run-status.json', state)
            allow_native_codes(source)
            perform(args.stage, source, root, state)
            state['stages'][args.stage] = 'passed'
            write(root / 'safe/run-status.json', state)
    except Exception as error:
        if 'root' in locals() and (root / 'safe').is_dir():
            try:
                record_rejection(root, 'stage', error, 'unknown')
            except Exception:
                pass
        if entered and state is not None and 'args' in locals() and args.stage in STAGES:
            state['stages'][args.stage] = 'failed'
            try:
                write(root / 'safe/run-status.json', state)
            except Exception:
                pass
        print('FORWARD_STAGE_REJECTED')
        return 1
    print('FORWARD_STAGE_VERIFIED')
    return 0


if __name__ == '__main__':
    sys.exit(main())
