import type { HostStatusSample } from './presentation.ts';
const sample = (change: Partial<HostStatusSample> = {}): HostStatusSample => ({
  source: 'synthetic', feed: 'fresh', phase: 'connected', cleanup: 'pending', terminal: false,
  cleanupUnknown: false, ageMs: 0, workers: 1, remainingMs: 118000, ...change
});
export const hostStatusFixtures: { label: string; input: unknown }[] = [
  { label: '未配線', input: { protocolVersion: 1, integration: 'not_connected', canStart: false, canStop: false, termination: 'unconfirmed' } },
  { label: '本人の承認待ち', input: sample({ phase: 'awaiting_approval', workers: 0, remainingMs: null, cleanup: 'not_started' }) },
  { label: '接続中の表示見本', input: sample() },
  { label: '更新停止', input: sample({ ageMs: 4000 }) },
  { label: '非 terminal の EOF', input: sample({ feed: 'eof' }) },
  { label: '最終報告と EOF', input: sample({ feed: 'eof', phase: 'closed', terminal: true, cleanup: 'confirmed', workers: 0, remainingMs: 0 }) },
  { label: '回収未確認', input: sample({ phase: 'failed', terminal: true, cleanup: 'unknown', cleanupUnknown: true, workers: 0, remainingMs: 0 }) },
  { label: '未開始の失敗報告', input: sample({ phase: 'failed', terminal: true, cleanup: 'not_started', workers: 0, remainingMs: null }) },
  { label: '不正な入力', input: { source: 'synthetic', phase: 'invalid' } }
];
