import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createResidentController, type ResidentState } from '../src/resident/controller.ts';
import { decodePairingResponse, type PairingResult } from '../src/resident/model.ts';
import { residentPairingMessage } from '../src/resident/presentation.ts';

const observation = (registration = 'disconnected', registrationReason?: string) => ({
  protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision: 1, validForMs: 3000,
  standby: registration === 'needs_action' ? 'on' : 'off', registration,
  ...(registrationReason ? { registrationReason } : {}), caller: null, work: 'idle', cleanup: 'confirmed',
  canRequestOn: registration !== 'needs_action', canRequestOff: registration === 'needs_action', transition: null,
  standbyUntil: null, standbyEndingSoon: false, standbyExpired: false, previousCleanup: 'confirmed', sleepPrevention: 'inactive',
});

async function canPair(status: unknown) {
  let current: ResidentState | undefined;
  const controller = createResidentController({ invoke: command => command === 'get_resident_status' ? Promise.resolve(status) : Promise.resolve(undefined),
    onChange: state => { current = state; } });
  await controller.refresh();
  controller.dispose();
  return current?.canPair;
}

// なぜ必要か: 明示された拒否状態だけで出し、汎用の取得不能を未登録と誤認させない。
void test('registration button follows explicit refusal states, not generic source-unavailable', async () => {
  assert.equal(await canPair({ protocolVersion: 1, source: 'resident-owner', state: 'source-policy-rejected' }), true);
  assert.equal(await canPair({ protocolVersion: 1, source: 'resident-owner', state: 'source-host-key-unavailable' }), true);
  assert.equal(await canPair(observation('needs_action', 'identity_refused')), true);
  assert.equal(await canPair({ protocolVersion: 1, source: 'resident-owner', state: 'source-unavailable' }), false);
  assert.equal(await canPair({ protocolVersion: 1, source: 'resident-owner', state: 'source-startup-checking' }), false);
  assert.equal(await canPair(observation()), false);
  assert.equal(await canPair(observation('needs_action', 'connection_occupied')), false);
  assert.equal(await canPair({ protocolVersion: 1, source: 'unsupported' }), false);
});

// なぜ必要か: 二重クリックで二つ目を起動せず、Tauriへ引数なしで呼び、成功後に状態を取り直す。
void test('pairing disables while running, invokes with no arguments, and refreshes after success', async () => {
  let state: ResidentState | undefined;
  let finish: ((value: unknown) => void) | undefined;
  let statusReads = 0;
  const statusCalls: string[] = [];
  const pairingCalls: unknown[][] = [];
  const controller = createResidentController({ invoke: command => {
    statusCalls.push(command); statusReads++;
    return Promise.resolve({ protocolVersion: 1, source: 'resident-owner', state: 'source-policy-rejected' });
  }, pairingInvoke: (...args: []) => {
    pairingCalls.push(args);
    return new Promise(resolve => { finish = resolve; });
  }, onChange: next => { state = next; } });
  await controller.refresh();
  const pending = controller.requestPairing();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(state?.pairingBusy, true);
  assert.deepEqual(pairingCalls.at(-1), []);
  await controller.requestPairing();
  assert.equal(pairingCalls.length, 1);
  finish?.({ result: 'succeeded' });
  await pending;
  assert.equal(state?.pairingBusy, false);
  assert.equal(state?.pairingResult, 'succeeded');
  assert.equal(statusReads, 3);
  assert.deepEqual(statusCalls, ['get_resident_status', 'get_resident_status', 'get_resident_status']);
  controller.dispose();
});

// なぜ必要か: 成功後のOFFはRustではなく、同じcontroller requestId列から一度だけ送る。
void test('successful pairing reads the new state and submits OFF with the controller request ID', async () => {
  const startup = { protocolVersion: 1, source: 'resident-owner', state: 'source-policy-rejected' };
  let current: unknown = startup, reads = 0;
  let state: ResidentState | undefined;
  const requests: unknown[] = [];
  const controller = createResidentController({ invoke: (command, args) => {
    if (command === 'get_resident_status') { reads++; return Promise.resolve(current); }
    requests.push(args.request);
    current = { ...observation(), revision: 2, transition: { requestId: args.request.requestId, intent: 'off', outcome: 'applied' } };
    return Promise.resolve({ protocolVersion: 1, requestId: args.request.requestId, intent: 'off', disposition: 'accepted' });
  }, pairingInvoke: () => {
    current = observation('needs_action', 'identity_refused');
    return Promise.resolve({ result: 'succeeded' });
  }, onChange: next => { state = next; } });
  try {
    await controller.refresh();
    await controller.requestPairing();
    assert.deepEqual(requests, [{ protocolVersion: 1, requestId: 1, expectedRevision: 1, intent: 'off' }]);
    assert.equal(reads, 3);
    assert.equal(state?.pairingResult, 'succeeded');
  } finally { controller.dispose(); }
});

// なぜ必要か: 登録時に待機OFFだったMacへ不要なintentを送らない。
void test('successful pairing does not submit OFF when the refreshed standby is off', async () => {
  const startup = { protocolVersion: 1, source: 'resident-owner', state: 'source-policy-rejected' };
  let current: unknown = startup;
  let state: ResidentState | undefined;
  const requests: unknown[] = [];
  const controller = createResidentController({ invoke: (command, args) => {
    if (command === 'get_resident_status') return Promise.resolve(current);
    requests.push(args.request);
    return Promise.resolve(undefined);
  }, pairingInvoke: () => {
    current = observation();
    return Promise.resolve({ result: 'succeeded' });
  }, onChange: next => { state = next; } });
  try {
    await controller.refresh();
    await controller.requestPairing();
    assert.deepEqual(requests, []);
    assert.equal(residentPairingMessage(state!.pairingResult!), 'この Mac を登録しました。待機を ON にすると新しい登録で始まります');
  } finally { controller.dispose(); }
});

// なぜ必要か: 未完了・拒否結果を登録成功と誤認して待機状態を変えない。
void test('pairing outcomes other than success never submit OFF', async () => {
  for (const result of ['cancelled', 'rejected', 're_registration_required', 'blocked', 'unknown', 'local_apply_failed', 'browser_failed', 'busy', 'unavailable'] as const) {
    const startup = { protocolVersion: 1, source: 'resident-owner', state: 'source-policy-rejected' };
    let current: unknown = startup;
    const requests: unknown[] = [];
    const controller = createResidentController({ invoke: (command, args) => {
      if (command === 'get_resident_status') return Promise.resolve(current);
      requests.push(args.request);
      return Promise.resolve(undefined);
    }, pairingInvoke: () => {
      current = observation('needs_action', 'identity_refused');
      return Promise.resolve({ result });
    }, onChange: () => {} });
    try {
      await controller.refresh();
      await controller.requestPairing();
      assert.deepEqual(requests, [], result);
    } finally { controller.dispose(); }
  }
});

// なぜ必要か: 20分のcallback待ち中に登録開始時の3秒観測期限が画面をsealedにしない。
void test('pairing outlives the observation without retaining its authority', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  let state: ResidentState | undefined;
  let finish: ((value: unknown) => void) | undefined;
  const controller = createResidentController({ invoke: command => Promise.resolve(command === 'get_resident_status'
    ? observation('needs_action', 'identity_refused') : undefined), pairingInvoke: () => new Promise(resolve => { finish = resolve; }),
  onChange: next => { state = next; }, now: () => 1000 });
  await controller.refresh();
  const pending = controller.requestPairing();
  await new Promise(resolve => setImmediate(resolve));
  t.mock.timers.tick(3001);
  assert.equal(state?.pairingBusy, true);
  assert.equal(state?.observation, null);
  finish?.({ result: 'cancelled' });
  await pending;
  assert.equal(state?.pairingResult, 'cancelled');
  controller.dispose();
});

// なぜ必要か: WebViewは固定の10種類だけを解釈し、余分な診断や未知の結果を表示しない。
void test('all pairing results have the specified Japanese message and strict response shape', () => {
  const messages: Record<PairingResult, string> = {
    succeeded: 'この Mac を登録しました。待機を ON にすると新しい登録で始まります', cancelled: '登録を取り消しました',
    rejected: '登録できませんでした。もう一度お試しください',
    re_registration_required: '登録の内容が変わりました。もう一度登録してください',
    blocked: 'この Mac の登録は今は置き換えられません',
    unknown: '登録の結果を確かめられませんでした。もう一度「登録する」を押すと続きを確かめます',
    local_apply_failed: '登録は済みましたが、この Mac に保存できませんでした。もう一度「登録する」を押してください',
    browser_failed: 'ブラウザを開けませんでした', busy: '別の登録が実行中です', unavailable: 'この Mac では登録を始められません',
  };
  for (const [result, message] of Object.entries(messages) as [PairingResult, string][]) {
    assert.equal(decodePairingResponse({ result }), result);
    assert.equal(residentPairingMessage(result), message);
  }
  assert.equal(decodePairingResponse({ result: 'succeeded', diagnostics: 'private' }), undefined);
  assert.equal(decodePairingResponse({ result: 'future' }), undefined);
});
