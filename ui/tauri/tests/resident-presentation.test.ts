import { test } from 'node:test';
import assert from 'node:assert/strict';
import { presentResident } from '../src/resident/presentation.ts';
import type { Observation } from '../src/resident/model.ts';

const observed = (changes: Partial<Observation> = {}): Observation => ({
  revision: 1, validForMs: 5000, standby: 'off', registration: 'disconnected', caller: 'none',
  work: 'idle', cleanup: 'confirmed', canRequestOn: true, canRequestOff: false, transition: null, ...changes
});

// 状態が読めないときに、接続や待機ONと同じ見た目を出すと、利用者が安全だと誤解する。
void test('an absent observation is shown as unconfirmed, never as a neutral or connected state', () => {
  const view = presentResident(null, false);
  assert.equal(view.tone, 'unconfirmed');
  assert.equal(view.icon, 'warning');
  assert.equal(view.heading, '常駐ホストの状態を確認できません');
  assert.equal(presentResident(null, true).tone, 'unconfirmed');
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
  assert.equal(presentResident(observed({ standby: 'unknown', registration: 'unknown', work: 'unknown', cleanup: 'unknown' }), false).detail,
    '待機: 未確認 / 接続: 未確認 / 作業: 未確認 / 回収: 未確認');
  assert.equal(/[a-z]/.test(presentResident(observed({ registration: 'connecting', work: 'preparing' }), false).detail), false);
});

// 青は「接続中」を表す。待機ONでも、認証済みの接続を観測するまでは接続済みに見せない。
void test('standby on without an authenticated registration is not shown as connected', () => {
  for (const registration of ['disconnected', 'connecting', 'unknown'] as const) {
    const view = presentResident(observed({ standby: 'on', registration }), false);
    assert.deepEqual([view.tone, view.icon, view.heading], ['neutral', 'checking', '待機ONを確認']);
  }
});
