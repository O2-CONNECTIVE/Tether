import type { Observation } from './model.ts';

export interface ResidentPresentation {
  heading: string;
  detail: string;
  tone: 'neutral' | 'connected' | 'stopping' | 'unconfirmed';
  icon: 'checking' | 'neutral' | 'warning' | 'active';
}
const standby: Record<Observation['standby'], string> = { off: 'OFF', on: 'ON', stopping: '停止中', unknown: '未確認' };
const registration: Record<Observation['registration'], string> = { disconnected: '未接続', connecting: '接続を確認中', authenticated: '認証済み', unknown: '未確認' };
const work: Record<Observation['work'], string> = { idle: '作業なし', preparing: '準備中', running: '実行中', stopping: '停止中', completed: '完了', failed: '失敗', unknown: '未確認' };
const cleanup: Record<Observation['cleanup'], string> = { confirmed: '回収確認済み', pending: '回収待ち', unknown: '要清掃' };
// Pure display mapping. It grants nothing: missing evidence stays unconfirmed,
// and a pending change is never shown as a settled state.
export function presentResident(o: Observation | null, pending: boolean, cleanupUnknown = false): ResidentPresentation {
  if (cleanupUnknown || o?.cleanup === 'unknown') return { heading: '要清掃: 常駐ホストの回収を確認できません',
    detail: '回収を確かめられないため、停止の成功を示しません。', tone: 'unconfirmed', icon: 'warning' };
  if (!o) return { heading: '常駐ホストの状態を確認できません', detail: '未確認。接続・停止・回収の成功を示しません。', tone: 'unconfirmed', icon: 'warning' };
  const detail = `待機: ${standby[o.standby]} / 接続: ${registration[o.registration]} / 作業: ${work[o.work]} / 回収: ${cleanup[o.cleanup]}`;
  if (pending) return { heading: '待機の変更を確認中', detail, tone: 'neutral', icon: 'checking' };
  // 青は「接続中」。待機ONでも認証済みの接続を観測するまでは接続済みに見せない。
  if (o.standby === 'on') return o.registration === 'authenticated' ? { heading: '待機ONを確認', detail, tone: 'connected', icon: 'active' }
    : { heading: '待機ONを確認', detail, tone: 'neutral', icon: 'checking' };
  if (o.standby === 'off') return { heading: '待機OFFを確認', detail, tone: 'neutral', icon: 'neutral' };
  return { heading: '待機停止を確認中', detail, tone: 'stopping', icon: 'checking' };
}
