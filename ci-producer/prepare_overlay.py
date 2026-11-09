#!/usr/bin/env python3
"""Prepare compile-only actual-producer fixture. Never starts the helper.

Updating SOURCE_SHA requires review of the new production run function; the
single loader-expression replacement must remain the only modified bytes.
"""
import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
parser = argparse.ArgumentParser()
parser.add_argument('--backend', type=Path, default=ROOT.parent / 'backend')
BACKEND = parser.parse_args().backend.resolve()
if not (BACKEND / 'go.mod').is_file():
    raise SystemExit('Expected repository backend; pass --backend for isolated review copy')
SOURCE_SHA = 'f79bcb3fcf2abc20aed97d895afd38c55f4b3919551ec97fcbf7d844dcaae920'
WORK_ENTRY_SHA = '2fd42dda65cdad708632c7d65ce12bc11819c88732b694e2d3d51f0ee2567dfd'
CLI_ENTRY_SHA = 'c7420e9cfb1e6a7527b48152a384e9eb33266684bcfea4c509a9120af57aa646'
source = BACKEND / 'internal/residentpipe/run_darwin.go'
raw = source.read_bytes()
if hashlib.sha256(raw).hexdigest() != SOURCE_SHA:
    raise SystemExit('Production run changed: independent review and pin update required')
old = b'residentmac.LoadStandby(ctx, protectedPolicy)'
new = b'ciLoadStandby(ctx, protectedPolicy)'
if raw.count(old) != 1:
    raise SystemExit('Expected exactly one loader expression')
out = ROOT / 'generated'
out.mkdir(exist_ok=True)
run = out / 'run_darwin.go'
run.write_bytes(raw.replace(old, new))
assert run.read_bytes().replace(new, old) == raw
mapping = {str(source): str(run)}

# These additional compiler-only observations preserve every original operation.
# Source-pin changes require independent review, never a permissive fallback.
def native_overlay(relative, expected, substitutions):
    source = BACKEND / relative
    raw = source.read_bytes()
    if hashlib.sha256(raw).hexdigest() != expected:
        raise SystemExit('Native diagnostic source changed: independent review required')
    modified = raw
    for old, new in substitutions:
        if modified.count(old) != 1:
            raise SystemExit('Native diagnostic seam is not unique')
        modified = modified.replace(old, new)
    restored = modified
    for old, new in reversed(substitutions):
        if restored.count(new) != 1:
            raise SystemExit('Native diagnostic seam is not reversible')
        restored = restored.replace(new, old)
    if restored != raw:
        raise SystemExit('Native diagnostic changed original bytes')
    target = out / (source.parent.name + '_' + source.name)
    target.write_bytes(modified)
    mapping[str(source)] = str(target)
native_overlay('internal/macagent/process.go', 'd308ca98b92d0d9b76f05ea9a05bac333bc1ca5eb2e7698121e67c0d784f5104', [(b'type process struct {', b'type process struct {\n\tciCapture ciNativeCapture // compiler-only bounded cached-state evidence'), (b'func (p *process) Terminate(ctx context.Context) error {\n\tp.term.Lock()\n\tdefer p.term.Unlock()', b'func (p *process) Terminate(ctx context.Context) (ciResult error) {\n\tp.term.Lock()\n\tdefer p.term.Unlock()\n\tdefer func() { if ciPanic := recover(); ciPanic != nil { p.ciCapture.last = CINativeSnapshot{}; panic(ciPanic) }; p.ciCaptureCleanup("terminate", ciResult) }()'), (b'func (p *process) Release(context.Context) error {\n\tp.term.Lock()\n\tdefer p.term.Unlock()', b'func (p *process) Release(_ context.Context) (ciResult error) {\n\tp.term.Lock()\n\tdefer p.term.Unlock()\n\tdefer func() { if ciPanic := recover(); ciPanic != nil { p.ciCapture.last = CINativeSnapshot{}; panic(ciPanic) }; p.ciCaptureCleanup("release", ciResult) }()')])
native_overlay('internal/agentexec/run.go', '91794c8ef9a00cf2b33d30d25633ae3a0afebcb1ebd24975eb408f95c3835392', [(b'\n\ts.mu.Lock()\n\tdefer s.mu.Unlock()\n\tc.terminated = terr == nil', b'\n\tif diagnostic, ok := proc.(interface { CIFinalCleanupDiagnostic(string, bool, bool) }); ok {\n\t\tdiagnostic.CIFinalCleanupDiagnostic(c.id, terr != nil, rerr != nil)\n\t}\n\n\ts.mu.Lock()\n\tdefer s.mu.Unlock()\n\tc.terminated = terr == nil')])

packages = {'ci_native_capture.go': 'macagent', 'ci_native_capture_test.go': 'macagent', 'ci_native_observation_test.go': 'residentpipe', 'ci_producer_transport.go': 'agenttransport', 'ci_producer_transport_test.go': 'agenttransport', 'ci_producer_factory.go': 'residentmac',
            'ci_producer_entry.go': 'residentpipe', 'ci_gateway_test.go': 'residentpipe',
            'ci_work_bootstrap.go': 'residentclient', 'ci_work_child_test.go': 'residentpipe',
            'ci_mcp_test.go': 'residentpipe', 'ci_child_test.go': 'residentpipe',
            'ci_gui_producer_test.go': 'residentpipe', 'ci_connection_test.go': 'residentpipe', 'ci_observation_test.go': 'residentpipe', 'ci_diagnostic_test.go': 'residentpipe'}
for name, package in packages.items():
    fixture = ROOT / 'fixtures' / name
    if not fixture.is_file():
        raise SystemExit('Required fixture missing: ' + name)
    target = BACKEND / 'internal' / package / name
    if target.exists() or target.is_symlink():
        raise SystemExit('Virtual fixture target already exists: ' + name)
    mapping[str(target)] = str(fixture)
helper = BACKEND / 'cmd/ci-producer-helper'
helper.mkdir(parents=True, exist_ok=True)
if (helper / 'main.go').exists() or (helper / 'main.go').is_symlink():
    raise SystemExit('Virtual helper target already exists')
if not (ROOT / 'fixtures/ci_helper_main.go').is_file():
    raise SystemExit('Required helper fixture missing')
mapping[str(helper / 'main.go')] = str(ROOT / 'fixtures/ci_helper_main.go')
# Replace only the unavailable composition leaf. The CLI's main, argument
# contract, help, parser and shared owned lifecycle are compiled unchanged.
entry = BACKEND / 'cmd/tether-host/entry.go'
if not entry.is_file() or hashlib.sha256(entry.read_bytes()).hexdigest() != CLI_ENTRY_SHA:
    raise SystemExit('Production CLI entry changed: independent review required')
cli_fixture = ROOT / 'fixtures/ci_cli_entry.go'
if not cli_fixture.is_file():
    raise SystemExit('Required CLI composition fixture missing')
mapping[str(entry)] = str(cli_fixture)
work_entry = BACKEND / 'cmd/tether-work/entry.go'
if not work_entry.is_file() or hashlib.sha256(work_entry.read_bytes()).hexdigest() != WORK_ENTRY_SHA:
    raise SystemExit('Production work CLI entry changed: independent review required')
work_fixture = ROOT / 'fixtures/ci_work_entry.go'
if not work_fixture.is_file():
    raise SystemExit('Required work CLI composition fixture missing')
mapping[str(work_entry)] = str(work_fixture)
(ROOT / 'OVERLAY.json').write_text(json.dumps({'Replace': mapping}, indent=2) + '\n')
manifest = {str(Path(k).relative_to(BACKEND)): hashlib.sha256(Path(v).read_bytes()).hexdigest() for k, v in mapping.items()}
(out / 'overlay-manifest.json').write_text(json.dumps({'run_source_sha256': SOURCE_SHA, 'cli_entry_sha256': CLI_ENTRY_SHA, 'work_entry_sha256': WORK_ENTRY_SHA, 'files': manifest}, indent=2)+'\n')
print('Prepared overlay only; no helper or test executed')
