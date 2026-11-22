import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createResidentController, type ResidentState } from '../src/resident/controller.ts';
import { type ResidentInvoke } from '../src/resident/model.ts';
const sample = (change: Record<string, unknown> = {}) => ({ protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision: 1,
  validForMs: 3000, standby: 'off', registration: 'disconnected', caller: null, work: 'idle', cleanup: 'confirmed', canRequestOn: true, canRequestOff: false,
  transition: null, standbyUntil: null, standbyEndingSoon: false, standbyExpired: false, sleepPrevention: 'inactive', ...change });
const on = (change: Record<string, unknown> = {}) => sample({
  standby: 'on', registration: 'authenticated', canRequestOn: false, canRequestOff: true, ...change,
});
function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}
function capture(invoke: ResidentInvoke) {
  let state: ResidentState | undefined;
  const controller = createResidentController({ invoke, onChange: next => { state = next; }, pollMs: 10000 });
  return { controller, last: () => state };
}

void test('a hidden menu stop shares the request IDs and refreshes the page only after reopening', async () => {
  let observation = sample(), reads = 0;
  const requests: Parameters<ResidentInvoke>[1]['request'][] = [];
  const r = capture((command, { request }) => {
    if (command === 'get_resident_status') { reads++; return Promise.resolve(observation); }
    requests.push(request);
    const on = request.intent === 'on';
    observation = sample({ revision: observation.revision + 1, standby: on ? 'on' : 'off',
      registration: on ? 'authenticated' : 'disconnected', canRequestOn: !on, canRequestOff: on,
      transition: { requestId: request.requestId, intent: request.intent, outcome: 'applied' } });
    return Promise.resolve({ protocolVersion: 1, requestId: request.requestId, intent: request.intent, disposition: 'accepted' });
  });
  try {
    await r.controller.refresh(); await r.controller.request('on');
    await r.controller.setPageVisible(false);
    await Promise.all([r.controller.stopFromMenu(), r.controller.stopFromMenu(), r.controller.request('off')]);
    assert.equal(reads, 3);
    assert.equal(r.last()?.observation, null);
    assert.deepEqual(requests, [{ protocolVersion: 1, requestId: 1, expectedRevision: 1, intent: 'on' },
      { protocolVersion: 1, requestId: 2, expectedRevision: 2, intent: 'off' }]);
    await r.controller.setPageVisible(true);
    assert.equal(reads, 4); assert.equal(r.last()?.canOn, true);
  } finally { r.controller.dispose(); }
});

void test('menu stop supersedes a status read and reopening waits for its own observation', { timeout: 3000 }, async () => {
  const reads: ReturnType<typeof deferred<unknown>>[] = [];
  const reopenedRead = deferred<void>();
  const requests: Parameters<ResidentInvoke>[1]['request'][] = [];
  const r = capture((command, { request }) => {
    if (command === 'get_resident_status') {
      const read = deferred<unknown>();
      reads.push(read);
      if (reads.length === 4) reopenedRead.resolve();
      return read.promise;
    }
    requests.push(request);
    return Promise.resolve({ protocolVersion: 1, requestId: request.requestId, intent: 'off', disposition: 'accepted' });
  });
  try {
    const initial = r.controller.refresh();
    reads[0].resolve(on()); await initial;
    const oldRead = r.controller.refresh();
    const stop = r.controller.stopFromMenu();
    await r.controller.stopFromMenu();
    await r.controller.request('off');
    await r.controller.setPageVisible(false);
    await r.controller.setPageVisible(true);
    assert.equal(reads.length, 3);
    reads[1].resolve(on({ revision: 2 })); await oldRead;
    assert.equal(r.last()?.canOff, false);

    reads[2].resolve(on({ revision: 3, transition: { requestId: 9, intent: 'on', outcome: 'applied' } }));
    await reopenedRead.promise;
    assert.deepEqual(requests, [{ protocolVersion: 1, requestId: 10, expectedRevision: 3, intent: 'off' }]);
    assert.equal(r.last()?.observation, null);
    assert.equal(r.last()?.canOn, false);
    reads[3].resolve(sample({ revision: 4, transition: { requestId: 10, intent: 'off', outcome: 'applied' } }));
    await stop;
    assert.equal(r.last()?.canOn, true);
  } finally { r.controller.dispose(); }
});

void test('hidden menu stop refuses stale, unconfirmed, unavailable and already stopped status', async () => {
  const outcomes = [
    [on({ revision: 1 }), 'RESIDENT_REVISION_INVALID'],
    [on({ revision: 3, cleanup: 'unknown', canRequestOff: false }), 'RESIDENT_CLEANUP_UNKNOWN'],
    [{ protocolVersion: 1, source: 'resident-owner', state: 'source-unavailable' }, 'RESIDENT_SOURCE_LOST'],
    [sample({ revision: 3 }), 'RESIDENT_CHECKING'],
  ] as const;
  for (const [observation, diagnostic] of outcomes) {
    let reads = 0, writes = 0;
    const r = capture(command => {
      if (command === 'get_resident_status') return Promise.resolve(++reads === 1 ? on({ revision: 2 }) : observation);
      writes++; return Promise.resolve(undefined);
    });
    try {
      await r.controller.refresh(); await r.controller.setPageVisible(false); await r.controller.stopFromMenu();
      assert.equal(reads, 2);
      assert.equal(writes, 0);
      assert.equal(r.last()?.diagnostic, diagnostic);
    } finally { r.controller.dispose(); }
  }
});

void test('visible menu stop keeps an OFF rejection and does not retry the request', async () => {
  let reads = 0;
  const requests: Parameters<ResidentInvoke>[1]['request'][] = [];
  const r = capture((command, { request }) => {
    if (command === 'get_resident_status') return Promise.resolve(on({ revision: ++reads }));
    requests.push(request);
    return Promise.resolve({ protocolVersion: 1, requestId: request.requestId, intent: 'off', disposition: 'stale' });
  });
  try {
    await r.controller.refresh(); await r.controller.stopFromMenu();
    assert.deepEqual(requests, [{ protocolVersion: 1, requestId: 1, expectedRevision: 2, intent: 'off' }]);
    assert.equal(reads, 3);
    assert.equal(r.last()?.lastReceipt, 'stale');
    assert.equal(r.last()?.observation?.standby, 'on');
    assert.equal(r.last()?.canOff, true);
  } finally { r.controller.dispose(); }
});

void test('menu stop waits for an original ON receipt and reconciles its matching application while hidden', async () => {
  let reads = 0;
  let observation = sample();
  const receipt = deferred<unknown>();
  const requests: Parameters<ResidentInvoke>[1]['request'][] = [];
  const r = capture((command, { request }) => {
    if (command === 'get_resident_status') { reads++; return Promise.resolve(observation); }
    requests.push(request);
    return request.intent === 'on' ? receipt.promise
      : Promise.resolve({ protocolVersion: 1, requestId: request.requestId, intent: 'off', disposition: 'accepted' });
  });
  try {
    await r.controller.refresh();
    const request = r.controller.request('on');
    await r.controller.stopFromMenu();
    assert.equal(reads, 1);
    receipt.resolve({ protocolVersion: 1, requestId: 1, intent: 'on', disposition: 'accepted' });
    await request;
    assert.equal(r.last()?.pending, true);
    await r.controller.setPageVisible(false);
    await r.controller.stopFromMenu();
    assert.equal(reads, 3);
    assert.deepEqual(requests, [{ protocolVersion: 1, requestId: 1, expectedRevision: 1, intent: 'on' }]);
    observation = on({ revision: 2, transition: { requestId: 1, intent: 'on', outcome: 'applied' } });
    await r.controller.stopFromMenu();
    assert.equal(reads, 4);
    assert.deepEqual(requests, [{ protocolVersion: 1, requestId: 1, expectedRevision: 1, intent: 'on' },
      { protocolVersion: 1, requestId: 2, expectedRevision: 2, intent: 'off' }]);
  } finally { r.controller.dispose(); }
});

void test('a duplicate menu stop can reconcile a pending OFF but does not resubmit it', async () => {
  let observation = on(), reads = 0;
  const requests: Parameters<ResidentInvoke>[1]['request'][] = [];
  const r = capture((command, { request }) => {
    if (command === 'get_resident_status') { reads++; return Promise.resolve(observation); }
    requests.push(request);
    return Promise.resolve({ protocolVersion: 1, requestId: request.requestId, intent: 'off', disposition: 'accepted' });
  });
  try {
    await r.controller.refresh(); await r.controller.setPageVisible(false);
    await r.controller.stopFromMenu();
    assert.equal(r.last()?.pending, true);
    observation = on({ revision: 2, transition: { requestId: 1, intent: 'off', outcome: 'pending' } });
    await Promise.all([r.controller.stopFromMenu(), r.controller.stopFromMenu()]);
    assert.equal(reads, 3);
    assert.equal(requests.length, 1);
    observation = on({ revision: 3, transition: { requestId: 1, intent: 'off', outcome: 'rejected' } });
    await r.controller.stopFromMenu();
    assert.equal(r.last()?.pending, false);
    assert.equal(requests.length, 1);
    await r.controller.stopFromMenu();
    assert.deepEqual(requests, [{ protocolVersion: 1, requestId: 1, expectedRevision: 1, intent: 'off' },
      { protocolVersion: 1, requestId: 2, expectedRevision: 3, intent: 'off' }]);
  } finally { r.controller.dispose(); }
});
