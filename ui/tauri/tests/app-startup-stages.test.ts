import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classifyAppStartup } from '../native-smoke/app-startup-stages.ts';

function fixture(stages: string[]) {
  return { schema: 1, variant: 'host-startup-stage-1', purpose: 'diagnostic-hints-only',
    events: stages.map((stage, index) => ({ seq: index + 1, stage, elapsedUs: index * 100 })),
    attempted: stages.length, recorded: stages.length, dropped: 0, sealed: true, inFlight: 0 };
}
void test('同じ完全観測窓でWebView build前と初回page-load完了後を区別する', () => {
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start'])).phase, 'webview-build-not-observed');
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start', 'webview-build-ok', 'setup-return', 'first-page-load-finished'])).phase, 'first-page-load-finished');
});

void test('build成功はpage-loadやIPC成功へ昇格せず、page-loadとbuild-returnの順を固定しない', () => {
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start', 'webview-build-ok'])).phase, 'webview-built-page-load-not-observed');
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start', 'first-page-load-finished', 'webview-build-ok', 'setup-return'])).phase, 'first-page-load-finished');
  assert.equal(classifyAppStartup(fixture(['setup-enter', 'webview-build-start', 'webview-build-error'])).phase, 'webview-build-error');
});
void test('欠落・未完了・未知情報・順序不整合は原文を返さず観測不完全とする', () => {
  const base = fixture(['setup-enter', 'webview-build-start']);
  for (const value of [null, {}, { ...base, dropped: 1 }, { ...base, sealed: false }, { ...base, inFlight: 1 },
    { ...base, attempted: 3 }, { ...base, recorded: 3 }, { ...base, dropped: false },
    { ...base, url: 'PRIVATE_FIXTURE' }, fixture(['setup-enter', 'setup-return']),
    fixture(['setup-enter', 'webview-build-start', 'webview-build-error', 'webview-build-ok']),
    { ...base, events: [{ seq: 1, stage: 'setup-enter', elapsedUs: 0, title: 'PRIVATE_FIXTURE' }] },
    { ...base, events: [{ seq: 1, stage: 'setup-enter', elapsedUs: -1 }] },
    fixture(['setup-enter', 'setup-enter'])]) {
    const report = classifyAppStartup(value);
    assert.equal(report.phase, 'diagnostic-incomplete'); assert.deepEqual(report.events, []);
    assert.equal(JSON.stringify(report).includes('PRIVATE_FIXTURE'), false);
  }
});

function wryFixture(stages: string[], hook = true) {
  return { ...fixture(stages), variant: 'host-startup-stage-2', wryHook: hook,
    sealReason: 'requested', sealedAtUs: stages.length * 100 };
}
void test('Wry初期化のrequest・callback・waitを同一封印窓の固定段階へ分ける', () => {
  const start = ['setup-enter', 'webview-build-start'];
  const environment = [...start, 'environment-request'];
  assert.equal(classifyAppStartup(wryFixture(environment)).phase, 'environment-callback-not-observed');
  assert.equal(classifyAppStartup(wryFixture([...environment, 'environment-callback-ok'])).phase, 'environment-wait-return-not-observed');
  const ready = [...environment, 'environment-callback-ok', 'environment-wait-ok'];
  assert.equal(classifyAppStartup(wryFixture(ready)).phase, 'controller-request-not-observed');
  assert.equal(classifyAppStartup(wryFixture([...ready, 'controller-request'])).phase, 'controller-callback-not-observed');
  assert.equal(classifyAppStartup(wryFixture([...ready, 'controller-request', 'controller-callback-ok'])).phase, 'controller-wait-return-not-observed');
  assert.equal(classifyAppStartup(wryFixture([...ready, 'controller-request', 'controller-callback-ok', 'controller-wait-ok'])).phase, 'webview-post-controller-build-not-observed');
  assert.equal(classifyAppStartup(wryFixture([...environment, 'environment-callback-error', 'environment-wait-error', 'webview-build-error'])).phase, 'webview-build-error');
  assert.equal(classifyAppStartup(wryFixture(start, false)).phase, 'webview-build-not-observed');
  const completed = [...ready, 'controller-request', 'controller-callback-ok', 'controller-wait-ok',
    'webview-build-ok', 'setup-return', 'first-page-load-finished'];
  assert.equal(classifyAppStartup({ ...wryFixture(completed), sealReason: 'complete' }).phase, 'first-page-load-finished');
  assert.equal(classifyAppStartup(wryFixture([...environment, 'environment-wait-error'])).phase, 'environment-wait-error');
  assert.equal(classifyAppStartup(wryFixture([...environment, 'webview-build-error'])).phase, 'webview-build-error');
});
void test('hook未設定・順序逆転・相反callback・封印後時刻を初期化証拠にしない', () => {
  const start = ['setup-enter', 'webview-build-start'];
  for (const value of [
    wryFixture([...start, 'environment-request'], false),
    wryFixture([...start, 'controller-request']),
    wryFixture([...start, 'environment-callback-ok']),
    wryFixture([...start, 'environment-request', 'environment-wait-ok']),
    wryFixture([...start, 'environment-request', 'environment-callback-ok', 'environment-callback-error']),
    wryFixture([...start, 'environment-request', 'environment-callback-error', 'environment-wait-ok']),
    { ...wryFixture(start), sealedAtUs: 0 },
    { ...wryFixture(start), sealReason: 'complete' },
    { ...wryFixture(start), sealReason: 'deadline' },
    { ...wryFixture(start), diagnostic: 'PRIVATE_FIXTURE' }
  ]) {
    const report = classifyAppStartup(value);
    assert.equal(report.phase, 'diagnostic-incomplete');
    assert.equal(JSON.stringify(report).includes('PRIVATE_FIXTURE'), false);
  }
});
