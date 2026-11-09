import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
import { createResidentController } from '../src/resident/controller.ts';
import type { Intent, Observation } from '../src/resident/model.ts';
const driver = readFileSync(new URL('../src-tauri/tests/resident-gui/drive.js', import.meta.url), 'utf8');

// Real controller + GUI driver, modeled ordered producer/Tracking boundary.
// This is not Go/Rust IPC or evidence of the particular failed hosted run.
async function replay(mode: 'old-stale' | 'ordered-stale' | 'accepted-pending' | 'unknown') {
  let lastId = 0, pending: number | null = null, firstOff = true, now = 0, ownerOffCalls = 0;
  let observation: Observation = { revision: 1, validForMs: 3000, standby: 'off', registration: 'disconnected',
    caller: null, work: 'idle', cleanup: 'confirmed', canRequestOn: true, canRequestOff: false, transition: null };
  const events: string[] = [], receipts: string[] = [], order: string[] = [];
  const dataset: Record<string, string> = {};
  const buttons = {
    '#resident-on': { disabled: true, click: () => { void controller.request('on'); } },
    '#resident-off': { disabled: true, click: () => { void controller.request('off'); } },
  };
  const controller = createResidentController({ now: () => 0, pollMs: 1000000,
    onChange: state => {
      const o = state.observation;
      Object.assign(dataset, { residentRequestId: String(state.lastRequestId), residentReceipt: state.lastReceipt ?? 'none',
        residentRevision: String(o?.revision ?? 0), residentStandby: o?.standby ?? 'unknown',
        residentRegistration: o?.registration ?? 'unknown', residentTransitionId: String(o?.transition?.requestId ?? 0),
        residentTransitionIntent: o?.transition?.intent ?? 'none', residentTransitionOutcome: o?.transition?.outcome ?? 'none' });
      buttons['#resident-on'].disabled = !state.canOn; buttons['#resident-off'].disabled = !state.canOff;
    }, invoke: (command, { request: r }) => {
      if (command === 'get_resident_status') return Promise.resolve({ protocolVersion: 1, source: 'resident-owner', state: 'source-observation', ...observation });
      const id = r.requestId!, intent = r.intent!;
      let disposition: string;
      if (id <= lastId || r.expectedRevision !== observation.revision) disposition = 'stale';
      else if (pending !== null) disposition = 'rejected';
      else {
        lastId = id; pending = id;
        if (intent === 'off' && firstOff) {
          firstOff = false;
          observation = { ...observation, revision: observation.revision + 1 }; // queued heartbeat
          if (mode.endsWith('stale')) {
            disposition = 'stale';
            const transition: Observation['transition'] = mode === 'ordered-stale'
              ? { requestId: id, intent, outcome: 'rejected' } : observation.transition;
            observation = { ...observation, revision: observation.revision + 1, transition };
            if (transition?.requestId === pending && transition.outcome === 'rejected') {
              pending = null; order.push(`evidence:${id}`);
            }
          } else { disposition = mode === 'unknown' ? 'unknown' : 'accepted'; ownerOffCalls++; }
        } else {
          disposition = 'accepted';
          if (intent === 'off') ownerOffCalls++;
          observation = applied(intent, id, observation.revision + 1); pending = null;
        }
      }
      receipts.push(`${intent}:${id}:${disposition}`); order.push(`receipt:${id}:${disposition}`);
      return Promise.resolve({ protocolVersion: 1, requestId: id, intent, disposition });
    } });
  try {
    await controller.refresh();
    await runInNewContext(driver, {
      performance: { now: () => now }, setTimeout: (f: () => void, ms: number) => { now += ms; queueMicrotask(f); },
      document: { querySelector: (s: string) => s === '#resident-panel' ? { dataset } : buttons[s as keyof typeof buttons] },
      window: { __TAURI__: { event: { emit: (_: string, value: string) => { events.push(value); return Promise.resolve(); } } } },
    });
    return { events, receipts, order, ownerOffCalls };
  } finally { controller.dispose(); }
}
function applied(intent: Intent, id: number, revision: number): Observation {
  return { revision, validForMs: 3000, standby: intent, registration: intent === 'on' ? 'authenticated' : 'disconnected',
    caller: null, work: 'idle', cleanup: 'confirmed', canRequestOn: intent === 'off', canRequestOff: intent === 'on',
    transition: { requestId: id, intent, outcome: 'applied' } };
}
void test('modeled pre-owner stale schedule requires same-ID rejection before its receipt', async () => {
  const old = await replay('old-stale');
  assert.equal(old.events.at(-1), 'failed:off:receipt');
  assert.deepEqual(old.receipts, ['on:1:accepted', 'off:2:stale', 'off:3:rejected']);
  assert.equal(old.ownerOffCalls, 0);
  const fixed = await replay('ordered-stale');
  assert.equal(fixed.events.at(-1), 'passed');
  assert.deepEqual(fixed.receipts, ['on:1:accepted', 'off:2:stale', 'off:3:accepted']);
  assert.ok(fixed.order.indexOf('evidence:2') < fixed.order.indexOf('receipt:2:stale'));
  assert.equal(fixed.ownerOffCalls, 1);
});
void test('accepted without completion and Unknown are never retried by actual controller/driver', async () => {
  for (const mode of ['accepted-pending', 'unknown'] as const) {
    const result = await replay(mode);
    assert.equal(result.events.includes('passed'), false);
    assert.equal(result.receipts.length, 2);
    assert.equal(result.ownerOffCalls, 1);
  }
});
