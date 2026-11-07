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
SOURCE_SHA = 'c8a359098dd8ee6ccd4e8a7eb531d63c459054544af3c6341946072300495ed5'
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
packages = {'ci_producer_transport.go': 'agenttransport', 'ci_producer_transport_test.go': 'agenttransport', 'ci_producer_factory.go': 'residentmac',
            'ci_producer_entry.go': 'residentpipe', 'ci_gateway_test.go': 'residentpipe',
            'ci_mcp_test.go': 'residentpipe', 'ci_child_test.go': 'residentpipe',
            'ci_connection_test.go': 'residentpipe', 'ci_observation_test.go': 'residentpipe', 'ci_diagnostic_test.go': 'residentpipe'}
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
(ROOT / 'OVERLAY.json').write_text(json.dumps({'Replace': mapping}, indent=2) + '\n')
manifest = {str(Path(k).relative_to(BACKEND)): hashlib.sha256(Path(v).read_bytes()).hexdigest() for k, v in mapping.items()}
(out / 'overlay-manifest.json').write_text(json.dumps({'run_source_sha256': SOURCE_SHA, 'files': manifest}, indent=2)+'\n')
print('Prepared overlay only; no helper or test executed')
