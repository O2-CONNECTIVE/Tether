import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createResidentController } from '../src/resident/controller.ts';
import { pairingTimeoutMs, type ResidentState } from '../src/resident/state.ts';

const startup = { protocolVersion: 1, source: 'resident-owner', state: 'source-policy-rejected' };
const waiting = { protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision: 2,
  validForMs: 3000, standby: 'on', registration: 'authenticated', caller: null, work: 'idle', cleanup: 'confirmed',
  canRequestOn: false, canRequestOff: true, transition: null, standbyUntil: null, standbyEndingSoon: false,
  standbyExpired: false, previousCleanup: 'confirmed', sleepPrevention: 'inactive' };

// なぜ必要か: 登録中のメニュー停止は結果種別に関係なく保持され、OFFを一度だけ送る必要がある。
void test('menu stop during pairing is applied once after every result kind', async () => {
  for (const result of ['cancelled', 'succeeded']) {
    let current: unknown = startup;
    let finish!: (value: unknown) => void;
    const intents: string[] = [];
    const controller = createResidentController({ invoke: (command, args) => {
      if (command === 'get_resident_status') return Promise.resolve(current);
      intents.push(args.request.intent!);
      return Promise.resolve({ protocolVersion: 1, requestId: args.request.requestId,
        intent: args.request.intent, disposition: 'accepted' });
    }, pairingInvoke: () => {
      current = waiting;
      return new Promise(resolve => { finish = resolve; });
    }, onChange: () => {} });
    try {
      await controller.refresh();
      const pairing = controller.requestPairing();
      await new Promise(resolve => setImmediate(resolve));
      await controller.stopFromMenu();
      finish({ result });
      await pairing;
      assert.deepEqual(intents, ['off'], result);
    } finally { controller.dispose(); }
  }
});

// なぜ必要か: 前回のbusy結果を新しい登録中に成功・失敗結果として誤表示しない。
void test('starting another pairing clears the previous result', async () => {
  let calls = 0, state: ResidentState | undefined;
  let finish!: (value: unknown) => void;
  const controller = createResidentController({ invoke: () => Promise.resolve(startup), pairingInvoke: () => {
    if (++calls === 1) return Promise.resolve({ result: 'busy' });
    return new Promise(resolve => { finish = resolve; });
  }, onChange: next => { state = next; } });
  try {
    await controller.refresh();
    await controller.requestPairing();
    assert.equal(state?.pairingResult, 'busy');
    const second = controller.requestPairing();
    try {
      await new Promise(resolve => setImmediate(resolve));
      assert.equal(state?.pairingResult, null);
      assert.equal(state?.pairingBusy, true);
    } finally {
      finish({ result: 'cancelled' });
      await second;
    }
  } finally { controller.dispose(); }
});

// なぜ必要か: 20分5秒で応答待ちを閉じ、unavailable表示と再操作可能なボタンへ戻す。
void test('pairing timeout returns unavailable and re-enables the button', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  let now = 10, state: ResidentState | undefined;
  const controller = createResidentController({ invoke: () => Promise.resolve(startup),
    pairingInvoke: () => new Promise(() => {}), onChange: next => { state = next; }, now: () => now });
  try {
    await controller.refresh();
    const pending = controller.requestPairing();
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(state?.pairingBusy, true);
    now += pairingTimeoutMs;
    t.mock.timers.tick(pairingTimeoutMs);
    await pending;
    assert.equal(state?.pairingResult, 'unavailable');
    assert.equal(state?.pairingBusy, false);
    assert.equal(state?.canPair, true);
  } finally { controller.dispose(); }
});
