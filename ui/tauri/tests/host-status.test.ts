import { test } from 'node:test';
import assert from 'node:assert/strict';
import { presentHostStatus } from '../src/host-status/presentation.ts';

void test('unwired and unknown objects cannot become connected or leak raw values', () => {
  for (const input of [undefined, null, [], { protocolVersion: 1, integration: 'not_connected', canStart: false, canStop: false, termination: 'unconfirmed' }, { phase: 'PRIVATE_SENTINEL' }]) {
    const view = presentHostStatus(input);
    assert.equal(view.heading, '状態は未確認です');
    assert.equal(view.workers, '—');
    assert.equal(view.remaining, '—');
    assert.equal(JSON.stringify(view).includes('PRIVATE_SENTINEL'), false);
  }
});

const sample = (changes: Record<string, unknown> = {}) => ({
  source: 'synthetic', feed: 'fresh', phase: 'connected', cleanup: 'pending',
  terminal: false, cleanupUnknown: false, ageMs: 0, workers: 1, remainingMs: 5000, ...changes
});
void test('fresh synthetic state exposes only observation, not consent or runtime authority', () => {
  const view = presentHostStatus(sample());
  assert.equal(view.heading, '接続中（表示見本）');
  assert.equal(view.workers, '1');
  assert.equal(view.remaining, '5 秒');
  assert.equal(view.cleanup, '回収待ち');
  assert.equal(presentHostStatus(sample({ phase: 'awaiting_approval', workers: 0, remainingMs: null })).heading, '本人の承認待ち（表示見本）');
});
void test('stale, EOF and unknown cannot retain current counts or show cleanup success', () => {
  for (const changes of [{ feed: 'stale' }, { ageMs: 3001 }, { feed: 'eof' }, { feed: 'invalid' }]) {
    const view = presentHostStatus(sample(changes));
    assert.equal(view.workers, '—'); assert.equal(view.remaining, '—');
    assert.notEqual(view.heading, '接続中（表示見本）');
  }
  const unknown = presentHostStatus(sample({ phase: 'failed', terminal: true, cleanup: 'unknown', cleanupUnknown: true, workers: 0, remainingMs: 0 }));
  assert.equal(unknown.heading, '回収を確認できません');
  assert.equal(unknown.tone, 'warning');
});
void test('terminal EOF is reported history, never a live connection or independent cleanup proof', () => {
  const view = presentHostStatus(sample({ feed: 'eof', phase: 'closed', terminal: true, cleanup: 'confirmed', workers: 0, remainingMs: 0 }));
  assert.equal(view.heading, '配信終了・最終報告の履歴');
  assert.equal(view.cleanup, '最終報告：回収確認済み（OS の独立確認ではありません）');
  assert.equal(view.workers, '—'); assert.equal(view.remaining, '—');
  assert.equal(view.tone, 'unknown');
});
void test('malformed and incompatible values fail closed without raw display', () => {
  for (const changes of [{ phase: { toString: () => 'connected' } }, { extra: 'PRIVATE_SENTINEL' }, { phase: 'PRIVATE_SENTINEL' }, { source: 'live' }, { feed: 'future' }, { ageMs: -1 }, { ageMs: NaN }, { workers: 2 }, { workers: true }, { remainingMs: Infinity }, { terminal: true }, { cleanup: 'confirmed' }, { cleanupUnknown: true }]) {
    assert.equal(presentHostStatus(sample(changes)).heading, '状態は未確認です');
    assert.equal(JSON.stringify(presentHostStatus(sample(changes))).includes('PRIVATE_SENTINEL'), false);
  }
});

void test('zero remaining time does not continue a connected display', () => {
  const view = presentHostStatus(sample({ remainingMs: 0 }));
  assert.equal(view.heading, '承認時間の終了・停止は未確認');
  assert.equal(view.workers, '—');
});
