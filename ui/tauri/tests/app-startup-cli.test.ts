import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import os from 'node:os';

function report(text: string): Record<string, unknown> {
  const value: unknown = JSON.parse(text);
  assert.ok(value && typeof value === 'object' && !Array.isArray(value));
  return value as Record<string, unknown>;
}

void test('CLIは一致buildを確認し、欠測traceを非0・秘密なしで返す', async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'rescue-startup-cli-'));
  try {
    const exe = path.join(directory, 'fixture.exe'); const manifest = path.join(directory, 'build.json');
    const bytes = Buffer.from('fixture-only-not-an-executable');
    await writeFile(exe, bytes);
    await writeFile(manifest, JSON.stringify({ schema: 1, variant: 'host-startup-diagnostic-1',
      sourceCommit: 'a'.repeat(40), features: ['custom-protocol', 'startup-diagnostics'], profile: 'release',
      exeSha256: createHash('sha256').update(bytes).digest('hex'), distribution: false }));
    const args = ['native-smoke/inspect-app-startup.ts', '--directory', directory, '--manifest', manifest, '--exe', exe, '--source', 'a'.repeat(40)];
    const run = (extra: string[]) => spawnSync(process.execPath, [...args, ...extra], { encoding: 'utf8', timeout: 5000 });
    const build = run(['--build-only']);
    assert.equal(build.status, 0, build.stderr); assert.equal(report(build.stdout).code, 'APP_STARTUP_BUILD_VERIFIED');
    const missing = run([]);
    assert.equal(missing.status, 1); assert.deepEqual(report(missing.stdout).startup,
      { schema: 1, purpose: 'diagnostic-hints-only', phase: 'diagnostic-incomplete', events: [] });
    assert.equal(missing.stderr, ''); assert.equal(missing.stdout.includes(directory), false);
    await writeFile(path.join(directory, 'app-startup-stages.json'), JSON.stringify({
      schema: 1, variant: 'host-startup-stage-1', purpose: 'diagnostic-hints-only',
      events: [{ seq: 1, stage: 'setup-enter', elapsedUs: 0 }],
      attempted: 1, recorded: 1, dropped: 0, sealed: true, inFlight: 0
    }));
    const collection = path.join(directory, 'collection.json');
    await writeFile(collection, JSON.stringify({ kind: 'windows-webview2-native-smoke', appStartupFileClosed: false }));
    assert.equal(report(run(['--collection', collection]).stdout).code, 'APP_STARTUP_INCOMPLETE');
    await writeFile(collection, JSON.stringify({ kind: 'windows-webview2-native-smoke', appStartupFileClosed: true }));
    assert.equal(report(run(['--collection', collection]).stdout).code, 'APP_STARTUP_COMPLETE');
    await writeFile(exe, 'PRIVATE_FIXTURE');
    const drift = run(['--build-only']);
    assert.equal(drift.status, 1); assert.equal(report(drift.stdout).code, 'APP_STARTUP_BUILD_REJECTED');
    assert.equal((drift.stdout + drift.stderr).includes('PRIVATE_FIXTURE'), false);
  } finally { await rm(directory, { recursive: true, force: true }); }
});

void test('隔離Wry buildの由来を保ち、hook無しtraceを診断完了にしない', async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'rescue-wry-startup-cli-'));
  try {
    const exe = path.join(directory, 'fixture.exe'); const manifest = path.join(directory, 'build.json');
    const bytes = Buffer.from('fixture-only-not-an-executable');
    await writeFile(exe, bytes);
    const wryTrace = { schema: 1, variant: 'wry-startup-trace-1', wryVersion: '0.57.1',
      archiveSha256: '759c50ddad0ef9913bb5496cb9d509a50433a3b78cb0238d61105526589cf0e9',
      patchSha256: 'c'.repeat(64), preparedSourceSha256: 'd'.repeat(64),
      generatedLockSha256: 'e'.repeat(64), wrySourceSha256: 'f'.repeat(64) };
    await writeFile(manifest, JSON.stringify({ schema: 1, variant: 'host-wry-startup-diagnostic-1',
      sourceCommit: 'a'.repeat(40), features: ['custom-protocol', 'startup-diagnostics'], profile: 'release',
      exeSha256: createHash('sha256').update(bytes).digest('hex'), distribution: false, wryTrace }));
    const collection = path.join(directory, 'collection.json');
    await writeFile(collection, JSON.stringify({ kind: 'windows-webview2-native-smoke', appStartupFileClosed: true }));
    const args = ['native-smoke/inspect-app-startup.ts', '--directory', directory, '--manifest', manifest,
      '--exe', exe, '--source', 'a'.repeat(40), '--collection', collection];
    const run = () => spawnSync(process.execPath, args, { encoding: 'utf8', timeout: 5000 });
    const trace = { schema: 1, variant: 'host-startup-stage-2', purpose: 'diagnostic-hints-only',
      events: [{ seq: 1, stage: 'setup-enter', elapsedUs: 0 }], attempted: 1, recorded: 1,
      dropped: 0, sealed: true, inFlight: 0, wryHook: false, sealReason: 'requested', sealedAtUs: 1 };
    await writeFile(path.join(directory, 'app-startup-stages.json'), JSON.stringify(trace));
    assert.equal(report(run().stdout).code, 'APP_STARTUP_INCOMPLETE');
    await writeFile(path.join(directory, 'app-startup-stages.json'), JSON.stringify({ ...trace, wryHook: true }));
    const result = run(); assert.equal(result.status, 0, result.stderr);
    const value = report(result.stdout);
    assert.equal(value.code, 'APP_STARTUP_COMPLETE');
    assert.deepEqual((value.build as Record<string, unknown>).wryTrace, wryTrace);
    assert.equal(result.stdout.includes(directory), false);
  } finally { await rm(directory, { recursive: true, force: true }); }
});
