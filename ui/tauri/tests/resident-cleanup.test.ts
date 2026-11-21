import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createResidentController, type ResidentState } from '../src/resident/controller.ts';
import type { ResidentInvoke } from '../src/resident/model.ts';
import { presentResident } from '../src/resident/presentation.ts';

const sample = (change: Record<string, unknown> = {}) => ({ protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision: 1,
  validForMs: 3000, standby: 'off', registration: 'disconnected', caller: null, work: 'idle', cleanup: 'confirmed', canRequestOn: true, canRequestOff: false, transition: null, ...change });
function capture(invoke: ResidentInvoke | undefined, options: { now?: () => number; timeoutMs?: number } = {}) {
  const states: ResidentState[] = [];
  const controller = createResidentController({ invoke, onChange: state => states.push(state), pollMs: 10000, ...options });
  return { controller, last: () => states.at(-1) };
}

void test('zero-validity cleanup uncertainty is shown as needs cleanup and blocks both operations', async () => {
  const requests: unknown[] = [];
  const r = capture((command, args) => {
    if (command === 'get_resident_status') return Promise.resolve(sample({ validForMs: 0, cleanup: 'unknown', canRequestOn: false }));
    requests.push(args.request);
    return Promise.resolve(undefined);
  });
  try {
    await r.controller.refresh();
    await r.controller.request('on');
    await r.controller.request('off');
    const state = r.last();
    const view = presentResident(state?.observation ?? null, state?.pending ?? false, state?.cleanupUnknown ?? false);
    assert.equal(state?.diagnostic, 'RESIDENT_CLEANUP_UNKNOWN');
    assert.equal(state?.cleanupUnknown, true);
    assert.equal(state?.canOn, false);
    assert.equal(state?.canOff, false);
    assert.equal(view.heading, '要清掃: 常駐ホストの回収を確認できません');
    assert.deepEqual(requests, []);
  } finally { r.controller.dispose(); }
});

void test('malformed cleanup-unknown response does not establish cleanup uncertainty', async () => {
  const r = capture(() => Promise.resolve({ ...sample({ validForMs: 0, cleanup: 'unknown', canRequestOn: false }), unexpected: true }));
  try {
    await r.controller.refresh();
    const state = r.last();
    const view = presentResident(state?.observation ?? null, state?.pending ?? false, state?.cleanupUnknown ?? false);
    assert.equal(state?.diagnostic, 'RESIDENT_INVALID_RESPONSE');
    assert.equal(state?.cleanupUnknown, false);
    assert.notEqual(view.heading, '要清掃: 常駐ホストの回収を確認できません');
    assert.equal(state?.canOn, false);
    assert.equal(state?.canOff, false);
  } finally { r.controller.dispose(); }
});

void test('cleanup unknown is sticky and cannot be revived by a later green source', async () => {
  let observation = sample(); let calls = 0;
  const r = capture(() => { calls++; return Promise.resolve(observation); });
  await r.controller.refresh();
  observation = sample({ revision: 2, cleanup: 'unknown', canRequestOn: false });
  await r.controller.refresh(); assert.equal(r.last()?.diagnostic, 'RESIDENT_CLEANUP_UNKNOWN');
  observation = sample({ revision: 3 }); await r.controller.refresh(); await r.controller.request('on');
  assert.equal(calls, 2); assert.equal(r.last()?.canOn, false); r.controller.dispose();
});
void test('cleanup uncertainty is shown as needs cleanup, never generic unconfirmed or success', async () => {
  let observation = sample();
  const r = capture(command => Promise.resolve(command === 'get_resident_status' ? observation : undefined));
  try {
    await r.controller.refresh();
    observation = sample({ revision: 2, cleanup: 'unknown', canRequestOn: false });
    await r.controller.refresh();
    const state = r.last();
    const view = presentResident(state?.observation ?? null, state?.pending ?? false, state?.cleanupUnknown ?? false);
    // An unproved cleanup must not look like a successful stop or an ordinary status expiry.
    assert.equal(view.heading, '要清掃: 常駐ホストの回収を確認できません');
    assert.equal(view.detail, '回収を確かめられないため、停止の成功を示しません。');
    assert.equal(view.tone, 'unconfirmed');
    assert.equal(view.icon, 'warning');
    assert.equal(state?.canOn, false);
    assert.equal(state?.canOff, false);
  } finally { r.controller.dispose(); }
});
void test('cleanup and other unknown states use separate diagnostics', async () => {
  const cases = [
    { change: { cleanup: 'unknown', canRequestOn: false }, diagnostic: 'RESIDENT_CLEANUP_UNKNOWN', cleanupUnknown: true },
    { change: { standby: 'unknown', canRequestOn: false }, diagnostic: 'RESIDENT_STATE_UNKNOWN', cleanupUnknown: false },
    { change: { transition: { requestId: 1, intent: 'on', outcome: 'unknown' }, canRequestOn: false },
      diagnostic: 'RESIDENT_STATE_UNKNOWN', cleanupUnknown: false },
  ];
  for (const { change, diagnostic, cleanupUnknown } of cases) {
    const r = capture(() => Promise.resolve(sample(change)));
    try {
      await r.controller.refresh();
      assert.equal(r.last()?.diagnostic, diagnostic);
      assert.equal(r.last()?.cleanupUnknown, cleanupUnknown);
    } finally { r.controller.dispose(); }
  }
});
void test('cleanup uncertainty stays latched after the view is invalidated', async () => {
  let observation = sample();
  const r = capture(() => Promise.resolve(observation));
  try {
    await r.controller.refresh();
    observation = sample({ revision: 2, cleanup: 'unknown', canRequestOn: false });
    await r.controller.refresh();
    r.controller.invalidate();
    const state = r.last();
    const view = presentResident(state?.observation ?? null, state?.pending ?? false, state?.cleanupUnknown ?? false);
    assert.equal(state?.cleanupUnknown, true);
    assert.equal(view.heading, '要清掃: 常駐ホストの回収を確認できません');
    assert.equal(state?.canOn, false);
    assert.equal(state?.canOff, false);
  } finally { r.controller.dispose(); }
});
void test('the current EOF response stays generically unconfirmed', async () => {
  let observation = sample();
  const r = capture(() => Promise.resolve(observation));
  try {
    await r.controller.refresh();
    // Rust's status after stdout EOF without a closed frame has expired validity and no actions.
    observation = sample({ validForMs: 0, canRequestOn: false, canRequestOff: false });
    await r.controller.refresh();
    const state = r.last();
    const view = presentResident(state?.observation ?? null, state?.pending ?? false, state?.cleanupUnknown ?? false);
    assert.equal(state?.diagnostic, 'RESIDENT_REVISION_INVALID');
    assert.equal(view.heading, '常駐ホストの状態を確認できません');
    assert.equal(view.tone, 'unconfirmed');
    assert.equal(state?.canOn, false);
    assert.equal(state?.canOff, false);
    assert.equal(state?.pending, false);
  } finally { r.controller.dispose(); }
});
void test('zero validity expires without changing the same revision action flags', async () => {
  let observation = sample({ canRequestOn: false });
  const r = capture(() => Promise.resolve(observation), { now: () => 10 });
  try {
    await r.controller.refresh();
    observation = sample({ validForMs: 0, canRequestOn: false });
    await r.controller.refresh();
    assert.equal(r.last()?.diagnostic, 'RESIDENT_OBSERVATION_EXPIRED');
    assert.equal(r.last()?.canOn, false);
    assert.equal(r.last()?.canOff, false);
  } finally { r.controller.dispose(); }
});
void test('elapsed IPC time, clock rollback and old revision cannot refresh authority', async () => {
  for (const mode of ['elapsed', 'clock', 'revision', 'same-revision']) {
    let time = 10, count = 0;
    const r = capture(() => {
      count++;
      if (count === 2) {
        if (mode === 'elapsed') time += 3000;
        if (mode === 'clock') time = 0;
      }
      return Promise.resolve(sample(mode === 'revision' ? { revision: count === 1 ? 2 : 1 }
        : mode === 'same-revision' && count === 2 ? { registration: 'connecting' } : {}));
    }, { now: () => time });
    await r.controller.refresh(); await r.controller.refresh(); assert.equal(r.last()?.canOn, false, mode); r.controller.dispose();
  }
});
