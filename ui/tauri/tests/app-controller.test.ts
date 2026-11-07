import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createAppController, type AppInvoke, type AppState } from '../src/app-controller.ts';

const unavailable = () => ({ protocolVersion: 1, source: 'mac-status', state: 'source-unavailable' });
const sample = (changes: Record<string, unknown> = {}) => ({ protocolVersion: 1, source: 'mac-status', state: 'synthetic-observation',
  view: { heading: '接続を確認中', detail: '閉じた表示試験です。', icon: 'checking' }, validForMs: 100,
  cleanupUnknown: false, terminalHistory: false, ...changes });
const settle = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));
function capture(invoke: AppInvoke | undefined, options: { timeoutMs?: number; now?: () => number } = {}) {
  const states: AppState[] = [];
  const controller = createAppController({ invoke, onChange: state => states.push(state), ...options });
  return { controller, states, last: () => states.at(-1) };
}
void test('Macは固定read requestだけを送り、未接続のまま繰り返し確認できる', async () => {
  const run = capture((command, args) => {
    assert.equal(command, 'get_status_presentation');
    assert.deepEqual(args, { request: { protocolVersion: 1 } });
    return Promise.resolve(unavailable());
  });
  await run.controller.refresh(); await run.controller.refresh();
  assert.equal(run.last()?.diagnostic, 'MAC_SOURCE_UNAVAILABLE');
  assert.equal(run.last()?.synthetic, false);
  run.controller.dispose();
});
void test('Windows選択後は元のstrict controllerとcommandをそのまま使う', async () => {
  const calls: string[] = [];
  const run = capture(command => {
    calls.push(command);
    return Promise.resolve(command === 'get_status_presentation' ? { protocolVersion: 1, source: 'windows-shell' }
      : { protocolVersion: 1, integration: 'not_connected', canStart: false, canStop: false, termination: 'unconfirmed' });
  });
  await run.controller.refresh(); run.controller.invalidate(); await run.controller.refresh();
  assert.deepEqual(calls, ['get_status_presentation', 'get_shell_status', 'get_shell_status']);
  assert.equal(run.last()?.diagnostic, 'SHELL_UNWIRED');
  run.controller.dispose();
});
void test('Macへの更新を装うWindows成功payloadや未知fieldは拒否する', async () => {
  for (const value of [null, {}, { ...unavailable(), platform: 'macos' }, { protocolVersion: 1, source: 'windows-shell', view: {} },
    sample({ validForMs: 3001 }), sample({ validForMs: NaN }), sample({ validForMs: 1.5 }),
    sample({ view: { heading: '<img>', detail: 'text', icon: 'success' } })]) {
    const run = capture(() => Promise.resolve(value));
    await run.controller.refresh();
    assert.equal(run.last()?.diagnostic, 'INVALID_RESPONSE');
    run.controller.dispose();
  }
});
void test('fixtureは一度だけ表示し、期限後と再確認で復活しない', async () => {
  let calls = 0;
  const run = capture(() => { calls += 1; return Promise.resolve(sample({ validForMs: 10 })); });
  await run.controller.refresh();
  assert.equal(run.last()?.diagnostic, 'MAC_SYNTHETIC_OBSERVATION');
  await settle(20); await run.controller.refresh();
  assert.equal(run.last()?.diagnostic, 'MAC_OBSERVATION_EXPIRED');
  assert.equal(run.last()?.view.icon, 'warning');
  assert.equal(calls, 1);
  run.controller.dispose();
});
void test('request経過時間を予算から引き、期限同値と時計異常を表示しない', async () => {
  for (const end of [100, 101, NaN, Infinity, -1]) {
    let reads = 0;
    const run = capture(() => Promise.resolve(sample()), { now: () => reads++ === 0 ? 0 : end });
    await run.controller.refresh();
    assert.equal(run.last()?.diagnostic, 'MAC_OBSERVATION_EXPIRED');
    assert.equal(run.states.some(state => state.kind === 'observation'), false);
    run.controller.dispose();
  }
});
void test('cleanup unknownは時間切れ・再確認後も警告を保持する', async () => {
  const run = capture(() => Promise.resolve(sample({ cleanupUnknown: true, validForMs: 0,
    view: { heading: '回収を確認できません', detail: 'unknown', icon: 'warning' } })));
  await run.controller.refresh(); await run.controller.refresh();
  assert.equal(run.last()?.view.heading, '回収を確認できません');
  assert.equal(run.last()?.synthetic, true);
  run.controller.dispose();
});
void test('EOF terminal履歴はcurrent表示の期限後も履歴として別保持する', async () => {
  const run = capture(() => Promise.resolve(sample({ terminalHistory: true, validForMs: 0,
    view: { heading: '状態配信終了・最終報告の履歴', detail: '現在の回収確認ではありません。', icon: 'neutral' } })));
  await run.controller.refresh();
  assert.equal(run.last()?.kind, 'unavailable');
  assert.equal(run.last()?.history?.heading, '状態配信終了・最終報告の履歴');
  run.controller.dispose();
});
void test('hide/resumeはpending応答を破棄し、同じpayloadを後の要求で救済しない', async () => {
  let reply: (value: unknown) => void = () => {};
  let calls = 0;
  const run = capture(() => ++calls === 1 ? new Promise(resolve => { reply = resolve; }) : Promise.resolve(sample()));
  const pending = run.controller.refresh();
  run.controller.invalidate(); await pending;
  reply(sample()); await Promise.resolve(); await run.controller.refresh();
  assert.equal(run.states.some(state => state.kind === 'observation'), false);
  assert.equal(run.last()?.diagnostic, 'MAC_OBSERVATION_EXPIRED');
  run.controller.dispose();
});
void test('timeout後の遅着と再要求による復活を防ぎ、重複要求しない', async () => {
  let reply: (value: unknown) => void = () => {};
  let calls = 0;
  const run = capture(() => ++calls === 1 ? new Promise(resolve => { reply = resolve; }) : Promise.resolve(sample()), { timeoutMs: 5 });
  const pending = run.controller.refresh(); await run.controller.refresh(); await pending;
  assert.equal(calls, 1); assert.equal(run.last()?.diagnostic, 'IPC_TIMEOUT');
  reply(sample()); await Promise.resolve(); await run.controller.refresh();
  assert.equal(run.states.some(state => state.kind === 'observation'), false);
  run.controller.dispose();
});
void test('破棄後は遅い成功やrefreshで通知しない', async () => {
  let reply: (value: unknown) => void = () => {};
  const run = capture(() => new Promise(resolve => { reply = resolve; }));
  const pending = run.controller.refresh(); run.controller.dispose(); await pending;
  const count = run.states.length; reply(sample()); await Promise.resolve(); await run.controller.refresh();
  assert.equal(run.states.length, count);
});
void test('取得失敗・不正route後は新しいfixtureも許可せずraw errorを転送しない', async () => {
  for (const fail of [() => Promise.reject(new Error('PRIVATE_DETAIL')), () => Promise.resolve({ source: 'fake' })]) {
    let calls = 0;
    const run = capture(() => ++calls === 1 ? fail() : Promise.resolve(sample()));
    await run.controller.refresh(); await run.controller.refresh();
    assert.equal(run.states.some(state => state.kind === 'observation'), false);
    assert.equal(JSON.stringify(run.states).includes('PRIVATE_DETAIL'), false);
    run.controller.dispose();
  }
});
void test('browser bridgeなしと非対応platformを明示し、Mac routeの変更を拒否する', async () => {
  const missing = capture(undefined); await missing.controller.refresh(); assert.equal(missing.last()?.diagnostic, 'BRIDGE_MISSING');
  missing.controller.dispose();
  const unsupported = capture(() => Promise.resolve({ protocolVersion: 1, source: 'unsupported' }));
  await unsupported.controller.refresh(); assert.equal(unsupported.last()?.diagnostic, 'PLATFORM_UNSUPPORTED'); unsupported.controller.dispose();
  let calls = 0;
  const changed = capture(() => Promise.resolve(++calls === 1 ? unavailable() : { protocolVersion: 1, source: 'windows-shell' }));
  await changed.controller.refresh(); await changed.controller.refresh(); assert.equal(changed.last()?.diagnostic, 'INVALID_RESPONSE'); changed.controller.dispose();
});
void test('IPC期限のtimerが遅れても期限同値以降の応答を採用しない', async () => {
  for (const elapsed of [2000, 2001]) {
    let reads = 0;
    let calls = 0;
    const run = capture(() => { calls += 1; return Promise.resolve(sample({ validForMs: 3000 })); },
      { timeoutMs: 2000, now: () => reads++ === 0 ? 0 : elapsed });
    await run.controller.refresh();
    assert.equal(run.last()?.diagnostic, 'IPC_TIMEOUT');
    assert.equal(run.states.some(state => state.kind === 'observation'), false);
    await run.controller.refresh();
    assert.equal(run.states.some(state => state.kind === 'observation'), false);
    assert.equal(calls, 2);
    run.controller.dispose();
  }
});
void test('IPC期限直前の応答は残った表示予算内だけ採用する', async () => {
  let reads = 0;
  const run = capture(() => Promise.resolve(sample({ validForMs: 3000 })),
    { timeoutMs: 2000, now: () => reads++ === 0 ? 0 : 1999 });
  try {
    await run.controller.refresh();
    assert.equal(run.last()?.diagnostic, 'MAC_SYNTHETIC_OBSERVATION');
  } finally { run.controller.dispose(); }
});
void test('期限後の履歴とcleanup fieldを新しい観測として保持しない', async () => {
  let reads = 0;
  const run = capture(() => Promise.resolve(sample({ validForMs: 3000, cleanupUnknown: true, terminalHistory: true,
    view: { heading: '期限後の履歴', detail: '古い報告', icon: 'warning' } })),
    { timeoutMs: 2000, now: () => reads++ === 0 ? 0 : 2000 });
  try {
    await run.controller.refresh();
    assert.equal(run.last()?.diagnostic, 'IPC_TIMEOUT');
    assert.equal(run.last()?.history, undefined);
    assert.equal(run.last()?.synthetic, false);
    assert.equal(run.last()?.view.heading, '状態の更新を確認できません');
  } finally { run.controller.dispose(); }
});
void test('通常Mac応答もtimerが遅れたIPC期限同値以降は採用せず再確認できる', async () => {
  for (const elapsed of [2000, 2001]) {
    let reads = 0;
    let calls = 0;
    const run = capture(() => { calls += 1; return Promise.resolve(unavailable()); },
      { timeoutMs: 2000, now: () => reads++ === 1 ? elapsed : 0 });
    try {
      await run.controller.refresh();
      assert.equal(run.last()?.diagnostic, 'IPC_TIMEOUT');
      assert.equal(run.states.some(state => state.diagnostic === 'MAC_SOURCE_UNAVAILABLE'), false);
      assert.equal(run.last()?.synthetic, false);
      await run.controller.refresh();
      assert.equal(run.last()?.diagnostic, 'MAC_SOURCE_UNAVAILABLE');
      assert.equal(calls, 2);
    } finally { run.controller.dispose(); }
  }
});
void test('通常Mac応答はIPC期限直前なら未接続として表示する', async () => {
  let reads = 0;
  const run = capture(() => Promise.resolve(unavailable()),
    { timeoutMs: 2000, now: () => reads++ === 0 ? 0 : 1999 });
  try {
    await run.controller.refresh();
    assert.equal(run.last()?.diagnostic, 'MAC_SOURCE_UNAVAILABLE');
    assert.equal(run.last()?.synthetic, false);
  } finally { run.controller.dispose(); }
});
