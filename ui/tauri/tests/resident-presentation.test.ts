import { test } from 'node:test';
import assert from 'node:assert/strict';
import { presentResident } from '../src/resident/presentation.ts';
import { decodeStatus, type Observation } from '../src/resident/model.ts';

const observed = (changes: Partial<Observation> = {}): Observation => ({
  revision: 1, validForMs: 5000, standby: 'off', registration: 'disconnected', caller: 'none',
  work: 'idle', cleanup: 'confirmed', canRequestOn: true, canRequestOff: false, transition: null,
  standbyUntil: null, standbyEndingSoon: false, standbyExpired: false, previousCleanup: 'confirmed', sleepPrevention: 'inactive', ...changes
});

// 状態が読めないときに、接続や待機ONと同じ見た目を出すと、利用者が安全だと誤解する。
void test('an absent observation is shown as unconfirmed, never as a neutral or connected state', () => {
  const view = presentResident(null, false);
  assert.equal(view.tone, 'unconfirmed');
  assert.equal(view.icon, 'warning');
  assert.equal(view.heading, '常駐ホストの状態を確認できません');
  assert.equal(presentResident(null, true).tone, 'unconfirmed');
});

void test('startup evidence has a closed shape and keeps cleanup uncertainty visible', () => {
  const startup = { protocolVersion: 1, source: 'resident-owner', state: 'source-policy-rejected' };
  assert.deepEqual(decodeStatus(startup), { source: 'resident-owner', observation: null, startup: 'policy-rejected' });
  assert.equal(decodeStatus({ ...startup, account: 'synthetic' }), undefined);
  assert.equal(decodeStatus({ ...startup, canRequestOn: true }), undefined);
  assert.equal(presentResident(null, false, false, 'checking').heading, '常駐ホストの起動結果を確認中');
  assert.equal(presentResident(null, false, false, 'policy-rejected').heading, '登録設定を確認してください');
  assert.deepEqual(presentResident(null, false, true, 'policy-rejected'), presentResident(null, false, true));
});

// なぜ要るか: Keychain鍵の読取不能だけを厳密に受け入れ、policy拒否や未確認と別の表示にする。
void test('host key unavailability decodes strictly and is shown as an unconfirmed warning', () => {
  const startup = { protocolVersion: 1, source: 'resident-owner', state: 'source-host-key-unavailable' };
  assert.deepEqual(decodeStatus(startup), { source: 'resident-owner', observation: null, startup: 'host-key-unavailable' });
  assert.equal(decodeStatus({ ...startup, account: 'synthetic' }), undefined);
  assert.deepEqual(presentResident(null, false, false, 'host-key-unavailable'), {
    heading: 'この Mac の鍵を読めません', detail: '登録し直してください', tone: 'unconfirmed', icon: 'warning',
  });
});

// 色だけで状態を区別しない: 待機ONとOFFは色・記号・文言の3つすべてが違う。
void test('standby on and off differ in tone, icon and heading', () => {
  const on = presentResident(observed({ standby: 'on', registration: 'authenticated' }), false);
  const off = presentResident(observed(), false);
  assert.deepEqual([on.tone, on.icon, on.heading], ['connected', 'active', '待機ONを確認']);
  assert.deepEqual([off.tone, off.icon, off.heading], ['neutral', 'neutral', '待機OFFを確認']);
});

// 受付は変更完了ではない: 変更の確認中は、直前の観測が ON でも確定した状態として見せない。
void test('a pending change or a stopping standby is shown as in progress, not as a settled state', () => {
  const pending = presentResident(observed({ standby: 'on' }), true);
  // 警告色は「停止中」の意味に限る。ON の要求の確認中を停止と読ませない。
  assert.deepEqual([pending.tone, pending.icon, pending.heading], ['neutral', 'checking', '待機の変更を確認中']);
  const stopping = presentResident(observed({ standby: 'stopping', work: 'stopping', cleanup: 'pending' }), false);
  assert.deepEqual([stopping.tone, stopping.icon, stopping.heading], ['stopping', 'checking', '待機停止を確認中']);
});

// 詳細行は利用者向けの日本語で出す。内部の値をそのまま見せない。
void test('the detail line uses Japanese labels and leaks no raw state value', () => {
  assert.equal(presentResident(observed({ standby: 'on', registration: 'authenticated', work: 'running', cleanup: 'pending' }), false).detail,
    '待機: ON / 接続: 認証済み / 作業: 実行中 / 回収: 回収待ち');
  assert.equal(presentResident(observed({ standby: 'unknown', registration: 'unknown', work: 'unknown', cleanup: 'pending' }), false).detail,
    '待機: 未確認 / 接続: 未確認 / 作業: 未確認 / 回収: 回収待ち');
  assert.equal(/[a-z]/.test(presentResident(observed({ registration: 'connecting', work: 'preparing' }), false).detail), false);
});

void test('cleanup unknown takes precedence over an observed standby off state', () => {
  const view = presentResident(observed({ cleanup: 'unknown', canRequestOn: false }), false);
  assert.deepEqual(view, {
    heading: '要清掃: 常駐ホストの回収を確認できません',
    detail: '回収を確かめられないため、停止の成功を示しません。',
    tone: 'unconfirmed',
    icon: 'warning',
  });
});

void test('a previous cleanup warning is shown only when its boot record is unknown', () => {
  const warning = '前回の遠隔作業が終わったか確認できませんでした。Mac を再起動すると、残っていた作業は止まります。';
  const unknown = presentResident(observed({ previousCleanup: 'unknown' }), false);
  const confirmed = presentResident(observed({ previousCleanup: 'confirmed' }), false);
  assert.equal(unknown.warning, warning);
  assert.equal(unknown.heading, confirmed.heading);
  assert.equal(confirmed.warning, undefined);
});

// 青は「接続中」を表す。待機ONでも、認証済みの接続を観測するまでは接続済みに見せない。
void test('standby on without an authenticated registration is not shown as connected', () => {
  for (const registration of ['disconnected', 'connecting', 'unknown'] as const) {
    const view = presentResident(observed({ standby: 'on', registration }), false);
    assert.deepEqual([view.tone, view.icon, view.heading], ['neutral', 'checking', '待機ONを確認']);
  }
});

// 一時的な切断は Go が再試行を続ける。接続済み（青）にも、利用者の操作が要る「要対応」にも見せない。
void test('standby on while reconnecting is shown as retrying, not as connected or as needing action', () => {
  const view = presentResident(observed({ standby: 'on', registration: 'reconnecting', work: 'stopping', cleanup: 'pending' }), false);
  assert.deepEqual([view.tone, view.icon, view.heading], ['neutral', 'checking', '待機ON・再接続中']);
  assert.match(view.detail, /作業: 停止中 \/ 回収: 回収待ち/);
  assert.match(view.detail, /待機と期限は続き、切断前の作業は停止・回収します。/);
});

void test('expiry reports a completed stop only after the owner confirms cleanup', () => {
  const stopping = presentResident(observed({ standby: 'stopping', work: 'stopping', cleanup: 'pending', standbyExpired: true }), false);
  assert.equal(stopping.heading, '待機期限に達しました。停止を確認中');
  assert.equal(presentResident(observed({ standbyExpired: true }), false).heading, '待機は期限で停止しました');
  assert.match(presentResident(observed({ cleanup: 'unknown', standbyExpired: true }), false).heading, /^要清掃/);
});

// 直らない失敗では Go が再試行をやめる。理由ごとに、何をすれば戻るかを利用者に示す。
// 身元の拒否は、ペア登録し直した後に常駐を起動し直すまで戻らない（起動中の常駐は変わった policy を拒む。docs/host-pairing.md）。
void test('needs action names the reason and the way back for each refusal', () => {
  const reasons = {
    identity_refused: 'このホストの身元が拒否されました。別の端末に置き換えられた可能性があります。もう一度ペア登録してから、アプリを終了して起動し直してください。',
    connection_occupied: '別の接続がこのホストを使っています。その接続を終えてから、待機をOFFにしてONにし直してください。',
    gateway_refused: 'サーバーが接続を拒否しました。設定を確認してから、待機をOFFにしてONにし直してください。',
  } as const;
  for (const [registrationReason, detail] of Object.entries(reasons)) {
    const view = presentResident(observed({ standby: 'on', registration: 'needs_action',
      registrationReason: registrationReason as keyof typeof reasons, canRequestOn: false, canRequestOff: true }), false);
    assert.deepEqual(view, { heading: '待機ON・要対応', detail: `自動での再接続を停止しました。${detail}`, tone: 'unconfirmed', icon: 'warning' });
  }
});

// 読み手が再接続中と要対応を「未接続」に潰すと、画面は Go の区別を出せない。受け取りから表示までを通して確かめる。
void test('the decoder keeps reconnecting and needs action apart from disconnected all the way to the screen', () => {
  const raw = { protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision: 1, validForMs: 3000, standby: 'on',
    caller: null, work: 'idle', cleanup: 'confirmed', canRequestOn: false, canRequestOff: true, transition: { requestId: 1, intent: 'on', outcome: 'applied' },
    standbyUntil: 1_900_000_000_000, standbyEndingSoon: false, standbyExpired: false, previousCleanup: 'confirmed', sleepPrevention: 'inactive' };
  const status = (change: Record<string, unknown>) => {
    const decoded = decodeStatus({ ...raw, ...change });
    assert.ok(decoded?.source === 'resident-owner' && decoded.observation);
    return decoded.observation;
  };
  assert.equal(presentResident(status({ registration: 'reconnecting' }), false).heading, '待機ON・再接続中');
  const needsAction = status({ registration: 'needs_action', registrationReason: 'connection_occupied' });
  assert.deepEqual([needsAction.registration, needsAction.registrationReason], ['needs_action', 'connection_occupied']);
  assert.equal(presentResident(needsAction, false).heading, '待機ON・要対応');
});
