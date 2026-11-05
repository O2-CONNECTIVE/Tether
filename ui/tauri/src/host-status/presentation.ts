// Synthetic display input only. This is NOT a wire protocol, authentication,
// stream decoder, sticky-history reducer, or grant/cleanup authority.
export interface HostStatusSample {
  source: 'synthetic';
  feed: 'fresh' | 'stale' | 'eof' | 'invalid';
  phase: 'preparing' | 'awaiting_approval' | 'connecting' | 'connected' | 'stopping' | 'closed' | 'failed';
  cleanup: 'not_started' | 'pending' | 'confirmed' | 'unknown';
  terminal: boolean;
  cleanupUnknown: boolean;
  ageMs: number;
  workers: number;
  remainingMs: number | null;
}
export interface HostStatusPresentation {
  heading: string; detail: string; workers: string; remaining: string; cleanup: string;
  tone: 'unknown' | 'observation' | 'warning';
}
const keys = 'ageMs,cleanup,cleanupUnknown,feed,phase,remainingMs,source,terminal,workers';
function valid(input: unknown): input is HostStatusSample {
  if (input === null || typeof input !== 'object' || Array.isArray(input)) return false;
  const m = input as Record<string, unknown>;
  if (Object.keys(m).sort().join(',') !== keys || m.source !== 'synthetic'
    || typeof m.feed !== 'string' || !['fresh', 'stale', 'eof', 'invalid'].includes(m.feed)
    || typeof m.phase !== 'string' || !['preparing', 'awaiting_approval', 'connecting', 'connected', 'stopping', 'closed', 'failed'].includes(m.phase)
    || typeof m.cleanup !== 'string' || !['not_started', 'pending', 'confirmed', 'unknown'].includes(m.cleanup)
    || typeof m.terminal !== 'boolean' || typeof m.cleanupUnknown !== 'boolean'
    || typeof m.ageMs !== 'number' || !Number.isSafeInteger(m.ageMs) || m.ageMs < 0
    || typeof m.workers !== 'number' || !Number.isInteger(m.workers) || m.workers < 0 || m.workers > 1
    || !(m.remainingMs === null || (typeof m.remainingMs === 'number' && Number.isInteger(m.remainingMs) && m.remainingMs >= 0 && m.remainingMs <= 86400000))) return false;
  if (m.terminal !== (m.phase === 'closed' || m.phase === 'failed')) return false;
  if ((m.cleanupUnknown || m.cleanup === 'unknown') && !(m.cleanupUnknown && m.cleanup === 'unknown' && m.phase === 'failed' && m.terminal)) return false;
  if (m.cleanup === 'confirmed' && !(m.terminal && m.workers === 0 && (m.remainingMs === null || m.remainingMs === 0))) return false;
  return true;
}
const unknown = (): HostStatusPresentation => ({ heading: '状態は未確認です', detail: '接続や回収は確認できません。', workers: '—', remaining: '—', cleanup: '未確認', tone: 'unknown' });
const labels: Record<HostStatusSample['phase'], string> = {
  preparing: '準備中', awaiting_approval: '本人の承認待ち', connecting: '接続を確認中',
  connected: '接続中', stopping: '停止を確認中', closed: '終了報告', failed: '失敗報告'
};
export function presentHostStatus(input: unknown): HostStatusPresentation {
  if (!valid(input)) return unknown();
  const view = unknown();
  if (input.cleanupUnknown) return { ...view, heading: '回収を確認できません', detail: '操作が終わっていても、安全に回収されたとは限りません。', cleanup: '回収未確認', tone: 'warning' };
  if (input.feed === 'invalid') return view;
  if (input.feed === 'eof') return { ...view,
    heading: input.terminal ? '配信終了・最終報告の履歴' : '状態配信が終了しました',
    detail: '現在の接続や agent の終了を示す証拠ではありません。',
    cleanup: input.terminal && input.cleanup === 'confirmed' ? '最終報告：回収確認済み（OS の独立確認ではありません）' : '未確認' };
  if (input.feed === 'stale' || input.ageMs > 3000) return { ...view, heading: '状態の更新を確認できません', detail: '古い報告を現在の状態として表示しません。' };
  if (!input.terminal && input.remainingMs === 0) return { ...view, heading: '承認時間の終了・停止は未確認', detail: '残り時間が 0 秒でも停止や回収の証明にはなりません。' };
  return { ...view, heading: labels[input.phase] + '（表示見本）', tone: 'observation',
    detail: input.terminal ? '最終報告です。OS 全体の回収を独立に確認したものではありません。' : '合成データの観測表示です。承認や実行を許可するものではありません。',
    workers: input.terminal ? '—' : String(input.workers),
    remaining: input.terminal || input.remainingMs === null ? '—' : `${Math.ceil(input.remainingMs / 1000)} 秒`,
    cleanup: input.cleanup === 'confirmed' ? '最終報告：回収確認済み（OS の独立確認ではありません）' : input.cleanup === 'not_started' ? '未開始' : '回収待ち' };
}
