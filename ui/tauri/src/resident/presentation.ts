import type { Observation, ResidentStartup } from './model.ts';

export interface ResidentPresentation {
  heading: string;
  detail: string;
  tone: 'neutral' | 'connected' | 'stopping' | 'unconfirmed';
  icon: 'checking' | 'neutral' | 'warning' | 'active';
  warning?: string;
}
const standby: Record<Observation['standby'], string> = { off: 'OFF', on: 'ON', stopping: '停止中', unknown: '未確認' };
const registration: Record<Observation['registration'], string> = { disconnected: '未接続', connecting: '接続を確認中', reconnecting: '再接続中',
  authenticated: '認証済み', needs_action: '要対応', unknown: '未確認' };
const needsActionDetail: Record<NonNullable<Observation['registrationReason']>, string> = {
  identity_refused: 'このホストの身元が拒否されました。別の端末に置き換えられた可能性があります。もう一度ペア登録してから、アプリを終了して起動し直してください。',
  connection_occupied: '別の接続がこのホストを使っています。その接続を終えてから、待機をOFFにしてONにし直してください。',
  gateway_refused: 'サーバーが接続を拒否しました。設定を確認してから、待機をOFFにしてONにし直してください。',
};
const work: Record<Observation['work'], string> = { idle: '作業なし', preparing: '準備中', running: '実行中', stopping: '停止中', completed: '完了', failed: '失敗', unknown: '未確認' };
const cleanup: Record<Observation['cleanup'], string> = { confirmed: '回収確認済み', pending: '回収待ち', unknown: '要清掃' };
// Pure display mapping. It grants nothing: missing evidence stays unconfirmed,
// and a pending change is never shown as a settled state.
const previousCleanupWarning = '前回の遠隔作業が終わったか確認できませんでした。Mac を再起動すると、残っていた作業は止まります。';
export function presentResident(o: Observation | null, pending: boolean, cleanupUnknown = false, startup: ResidentStartup | null = null): ResidentPresentation {
  const view = presentResidentState(o, pending, cleanupUnknown, startup);
  return o?.previousCleanup === 'unknown' ? { ...view, warning: previousCleanupWarning } : view;
}
function presentResidentState(o: Observation | null, pending: boolean, cleanupUnknown: boolean, startup: ResidentStartup | null): ResidentPresentation {
  if (cleanupUnknown || o?.cleanup === 'unknown') return { heading: '要清掃: 常駐ホストの回収を確認できません',
    detail: '回収を確かめられないため、停止の成功を示しません。', tone: 'unconfirmed', icon: 'warning' };
  if (!o && startup === 'checking') return { heading: '常駐ホストの起動結果を確認中',
    detail: '起動結果が届くまで待機を開始できません。', tone: 'neutral', icon: 'checking' };
  if (!o && startup === 'policy-rejected') return { heading: '登録設定を確認してください',
    detail: 'この Mac の登録設定に問題があります。設定を確認してからアプリを開き直してください。', tone: 'unconfirmed', icon: 'warning' };
  if (!o) return { heading: '常駐ホストの状態を確認できません', detail: '未確認。接続・停止・回収の成功を示しません。', tone: 'unconfirmed', icon: 'warning' };
  const detail = `待機: ${standby[o.standby]} / 接続: ${registration[o.registration]} / 作業: ${work[o.work]} / 回収: ${cleanup[o.cleanup]}`;
  if (pending) return { heading: '待機の変更を確認中', detail, tone: 'neutral', icon: 'checking' };
  // 青は「接続中」。待機ONでも認証済みの接続を観測するまでは接続済みに見せない。
  if (o.standby === 'on') {
    if (o.registration === 'authenticated') return { heading: '待機ONを確認', detail, tone: 'connected', icon: 'active' };
    if (o.registration === 'reconnecting') return { heading: '待機ON・再接続中',
      detail: `${detail}。待機と期限は続き、切断前の作業は停止・回収します。`, tone: 'neutral', icon: 'checking' };
    if (o.registration === 'needs_action') return { heading: '待機ON・要対応',
      detail: `自動での再接続を停止しました。${o.registrationReason ? needsActionDetail[o.registrationReason] : detail}`, tone: 'unconfirmed', icon: 'warning' };
    return { heading: '待機ONを確認', detail, tone: 'neutral', icon: 'checking' };
  }
  if (o.standby === 'off') return { heading: o.standbyExpired && o.cleanup === 'confirmed' && o.work === 'idle'
    ? '待機は期限で停止しました' : '待機OFFを確認', detail, tone: 'neutral', icon: 'neutral' };
  if (o.standby === 'stopping' && o.standbyExpired) return {
    heading: '待機期限に達しました。停止を確認中', detail, tone: 'stopping', icon: 'checking' };
  return { heading: '待機停止を確認中', detail, tone: 'stopping', icon: 'checking' };
}
