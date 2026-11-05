import type { HostStatusPresentation } from './presentation.ts';

interface WindowsHostPresentation {
  scope: 'windows-host-presentation'; version: 1;
  state: 'unwired' | 'unavailable'; authority: 'not_established';
  canStart: false; canStop: false; presentation: HostStatusPresentation;
}
// Input is the existing controller onChange state, NOT its raw IPC payload.
// This stateless display seam neither validates a wire nor retains fault history.
// Caller freshness is observation metadata, never authentication evidence.
// No authority-established variant exists in the current backend.
export function presentWindowsObservation(input: unknown): WindowsHostPresentation {
  const result: WindowsHostPresentation = {
    scope: 'windows-host-presentation', version: 1, state: 'unavailable', authority: 'not_established',
    canStart: false, canStop: false,
    presentation: { heading: '状態を取得できません', detail: '接続機能の状態を取得できませんでした。接続や通信終了は確認できません。', workers: '—', remaining: '—', cleanup: '未確認', tone: 'unknown' }
  };
  if (input === null || typeof input !== 'object' || Array.isArray(input)) return result;
  const record = input as Record<string, unknown>;
  if (Object.keys(record).sort().join(',') !== 'feed,scope,state,version'
    || record.scope !== 'windows-shell-observation' || record.version !== 1
    || typeof record.feed !== 'string' || !['fresh', 'stale', 'eof', 'invalid'].includes(record.feed)) return result;
  const state = record.state;
  if (state === null || typeof state !== 'object' || Array.isArray(state)) return result;
  const value = state as Record<string, unknown>;
  if (Object.keys(value).sort().join(',') !== 'diagnostic,kind') return result;
  if (value.kind === 'unavailable' && value.diagnostic === 'TERMINATION_UNCONFIRMED') {
    result.presentation.heading = '回収を確認できません';
    result.presentation.detail = '終了確認不能の診断です。EOF や画面終了は回収確認ではありません。';
    result.presentation.cleanup = '回収未確認';
    result.presentation.tone = 'warning';
    return result;
  }
  if (record.feed === 'fresh' && value.kind === 'not_connected' && value.diagnostic === 'SHELL_UNWIRED') {
    result.state = 'unwired';
    result.presentation.heading = 'バックエンド未接続';
    result.presentation.detail = '現在の shell は未配線です。開始・停止・終了確認は利用できません。';
  }
  return result;
}
