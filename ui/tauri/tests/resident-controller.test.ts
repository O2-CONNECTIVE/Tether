import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createResidentController, type ResidentState } from '../src/resident/controller.ts';
import { decodeStatus, decodeReceipt, type ResidentInvoke } from '../src/resident/model.ts';
const sample = (change: Record<string, unknown> = {}) => ({ protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision: 1,
  validForMs: 3000, standby: 'off', registration: 'disconnected', caller: null, work: 'idle', cleanup: 'confirmed', canRequestOn: true, canRequestOff: false, transition: null, ...change });
const settle = () => new Promise(resolve => setTimeout(resolve, 5));
function capture(invoke: ResidentInvoke | undefined, options: { now?: () => number; timeoutMs?: number } = {}) {
  const states: ResidentState[] = [];
  const controller = createResidentController({ invoke, onChange: state => states.push(state), pollMs: 10000, ...options });
  return { controller, states, last: () => states.at(-1) };
}
void test('fixed status and unbound/unsupported responses do not create authority', async () => {
  for (const source of ['unsupported', 'resident-owner']) {
    const r = capture((command, args) => {
      assert.equal(command, 'get_resident_status'); assert.deepEqual(args, { request: { protocolVersion: 1 } });
      return Promise.resolve(source === 'unsupported' ? { protocolVersion: 1, source } : { protocolVersion: 1, source, state: 'source-unavailable' });
    });
    await r.controller.refresh(); assert.equal(r.last()?.visible, source !== 'unsupported'); assert.equal(r.last()?.canOn, false);
    r.controller.dispose();
  }
});
void test('strict source contract rejects unknown fields, enums and inconsistent states', () => {
  assert.ok(decodeStatus(sample()));
  for (const changes of [{ extra: 'secret' }, { revision: 0 }, { validForMs: 3001 }, { validForMs: 0 }, { caller: '\n' },
    { canRequestOff: true }, { cleanup: 'unknown' }, { work: 'running' }, { standby: 'on' }, { transition: {} },
    { transition: { requestId: 1, intent: 'off', outcome: 'unknown' } }, { revision: Number.MAX_SAFE_INTEGER + 1 }]) {
    assert.equal(decodeStatus(sample(changes)), undefined);
  }
  assert.equal(decodeReceipt({ protocolVersion: 1, requestId: 1, intent: 'on', disposition: 'accepted', extra: 1 }, 1, 'on'), undefined);
});
void test('accepted receipt does not apply ON; matching observation permits OFF with new ID', async () => {
  let observation = sample(); const requests: unknown[] = [];
  const r = capture((command, args) => {
    if (command === 'get_resident_status') return Promise.resolve(observation);
    requests.push(args.request);
    return Promise.resolve({ protocolVersion: 1, requestId: args.request.requestId, intent: args.request.intent, disposition: 'accepted' });
  });
  await r.controller.refresh(); assert.equal(r.last()?.canOn, true);
  await r.controller.request('on'); assert.equal(r.last()?.pending, true); assert.equal(r.last()?.observation?.standby, 'off');
  assert.equal(r.last()?.canOff, false); await r.controller.request('on'); assert.equal(requests.length, 1);
  observation = sample({ revision: 2, standby: 'on', registration: 'authenticated', canRequestOn: false, canRequestOff: true,
    transition: { requestId: 1, intent: 'on', outcome: 'applied' } });
  await r.controller.refresh(); assert.equal(r.last()?.pending, false); assert.equal(r.last()?.canOff, true);
  await r.controller.request('off');
  assert.deepEqual(requests, [{ protocolVersion: 1, requestId: 1, expectedRevision: 1, intent: 'on' },
    { protocolVersion: 1, requestId: 2, expectedRevision: 2, intent: 'off' }]); r.controller.dispose();
});
void test('pending source transition disables operations and preserves request ID watermark', async () => {
  let observation = sample({ canRequestOn: false, transition: { requestId: 42, intent: 'on', outcome: 'pending' } });
  const requests: unknown[] = [];
  const r = capture((command, args) => {
    if (command === 'get_resident_status') return Promise.resolve(observation);
    requests.push(args.request); return Promise.resolve({ protocolVersion: 1, requestId: 43, intent: 'on', disposition: 'rejected' });
  });
  await r.controller.refresh(); assert.equal(r.last()?.pending, true); assert.equal(r.last()?.canOn, false);
  observation = sample({ revision: 2, transition: { requestId: 42, intent: 'on', outcome: 'rejected' } });
  await r.controller.refresh(); await r.controller.request('on');
  assert.deepEqual(requests, [{ protocolVersion: 1, requestId: 43, expectedRevision: 2, intent: 'on' }]); r.controller.dispose();
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
void test('invalidated or disposed pending reads and receipts never revive UI', async () => {
  let complete: (v: unknown) => void = () => {};
  const r = capture(() => new Promise(resolve => { complete = resolve; }));
  const read = r.controller.refresh(); r.controller.invalidate(); const count = r.states.length;
  complete(sample()); await read; assert.equal(r.states.length, count); assert.equal(r.last()?.canOn, false); r.controller.dispose();
  const s = capture(command => command === 'get_resident_status' ? Promise.resolve(sample()) : new Promise(resolve => { complete = resolve; }));
  await s.controller.refresh(); const write = s.controller.request('on'); s.controller.dispose(); const old = s.states.length;
  complete({ protocolVersion: 1, requestId: 1, intent: 'on', disposition: 'accepted' }); await write; assert.equal(s.states.length, old);
});
void test('timeout and mismatched receipt poison the view without automatic retry', async () => {
  for (const timeout of [true, false]) {
    let count = 0;
    const r = capture(command => {
      count++;
      return command === 'get_resident_status' ? Promise.resolve(sample()) : timeout ? new Promise(() => {})
        : Promise.resolve({ protocolVersion: 1, requestId: 99, intent: 'on', disposition: 'accepted' });
    }, { timeoutMs: 10 });
    await r.controller.refresh(); await r.controller.request('on'); await r.controller.refresh(); await settle();
    assert.equal(r.last()?.diagnostic, 'RESIDENT_INTENT_UNCONFIRMED'); assert.equal(count, 2); r.controller.dispose();
  }
});
void test('late IPC response cannot win a delayed timeout timer', async () => {
  for (const operation of ['read', 'intent']) {
    let time = 0;
    const r = capture(command => {
      if (command === 'get_resident_status') {
        if (operation === 'read') time = 2000;
        return Promise.resolve(sample());
      }
      time = 2000;
      return Promise.resolve({ protocolVersion: 1, requestId: 1, intent: 'on', disposition: 'accepted' });
    }, { now: () => time });
    await r.controller.refresh(); if (operation === 'intent') await r.controller.request('on');
    assert.equal(r.last()?.canOn, false); assert.equal(r.last()?.canOff, false); r.controller.dispose();
  }
});

void test('stale receipt identity survives fresh observation without resending an intent', async () => {
  let revision = 1; let calls = 0;
  const r = capture((command, args) => {
    if (command === 'get_resident_status') return Promise.resolve(sample({ revision }));
    calls++; revision++;
    return Promise.resolve({ protocolVersion: 1, requestId: args.request.requestId, intent: args.request.intent, disposition: 'stale' });
  });
  await r.controller.refresh(); assert.equal(r.last()?.lastReceipt, null); assert.equal(r.last()?.lastRequestId, 0);
  await r.controller.request('on');
  assert.equal(r.last()?.lastReceipt, 'stale'); assert.equal(r.last()?.lastRequestId, 1);
  assert.equal(r.last()?.observation?.revision, 2); assert.equal(r.last()?.pending, false); assert.equal(r.last()?.canOn, true);
  await r.controller.refresh(); assert.equal(calls, 1); assert.equal(r.last()?.lastRequestId, 1);
  r.controller.dispose();
});

void test('real mapper pending ON remains disabled until same request is authenticated and applied', async () => {
  let observation = sample(); const requests: unknown[] = [];
  const r = capture((command, args) => {
    if (command === 'get_resident_status') return Promise.resolve(observation);
    requests.push(args.request);
    observation = sample({ revision: 2, standby: 'unknown', registration: 'connecting', canRequestOn: false,
      transition: { requestId: 1, intent: 'on', outcome: 'pending' } });
    return Promise.resolve({ protocolVersion: 1, requestId: 1, intent: 'on', disposition: 'accepted' });
  });
  try {
    await r.controller.refresh(); await r.controller.request('on');
    assert.equal(r.last()?.diagnostic, 'RESIDENT_INTENT_PENDING');
    assert.equal(r.last()?.canOn, false); assert.equal(r.last()?.canOff, false);
    await r.controller.request('on'); await r.controller.request('off'); assert.equal(requests.length, 1);
    observation = sample({ revision: 3, standby: 'on', registration: 'authenticated', canRequestOn: false, canRequestOff: true,
      transition: { requestId: 1, intent: 'on', outcome: 'applied' } });
    await r.controller.refresh(); assert.equal(r.last()?.pending, false); assert.equal(r.last()?.canOff, true);
  } finally { r.controller.dispose(); }
});
void test('unknown outside exact mapper pending ON stays sealed, including mismatched pending identities', async () => {
  for (const changes of [{ cleanup: 'unknown' }, { registration: 'unknown' }, { work: 'unknown' }, { cleanup: 'pending' },
    { transition: null }, { transition: { requestId: 1, intent: 'on', outcome: 'unknown' } },
    { transition: { requestId: 2, intent: 'on', outcome: 'pending' } },
    { transition: { requestId: 1, intent: 'off', outcome: 'pending' } }]) {
    let observation = sample(); let calls = 0;
    const r = capture((command, args) => {
      calls++;
      if (command === 'get_resident_status') return Promise.resolve(observation);
      observation = sample({ revision: 2, standby: 'unknown', registration: 'connecting', canRequestOn: false,
        transition: { requestId: 1, intent: 'on', outcome: 'pending' }, ...changes });
      return Promise.resolve({ protocolVersion: 1, requestId: args.request.requestId, intent: args.request.intent, disposition: 'accepted' });
    });
    try {
      await r.controller.refresh(); await r.controller.request('on');
      assert.equal(r.last()?.canOff, false); assert.equal(r.last()?.observation, null);
      const before = calls; observation = sample({ revision: 3 }); await r.controller.refresh();
      assert.equal(calls, before); assert.equal(r.last()?.canOn, false);
    } finally { r.controller.dispose(); }
  }
});
