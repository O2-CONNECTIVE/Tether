import { useEffect, useRef, useState } from 'react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import { createResidentController, initialResidentState } from './controller.ts';
import { standbyHoursOptions, type ResidentInvoke, type StandbyHours } from './model.ts';
import { presentResident } from './presentation.ts';
import { StatusContent } from '../host-status/StatusContent.tsx';
export function ResidentPanel({ invoke }: { invoke: ResidentInvoke | undefined }) {
  const [state, setState] = useState(initialResidentState);
  const [hours, setHours] = useState<StandbyHours>(2);
  const controller = useRef<ReturnType<typeof createResidentController> | null>(null);
  useEffect(() => {
    const instance = createResidentController({ invoke, onChange: setState });
    controller.current = instance; void instance.refresh();
    const invalidate = () => instance.invalidate();
    document.addEventListener('visibilitychange', invalidate);
    window.addEventListener('pagehide', invalidate);
    const restore = (event: PageTransitionEvent) => { if (event.persisted) invalidate(); };
    window.addEventListener('pageshow', restore);
    return () => { instance.dispose(); controller.current = null;
      document.removeEventListener('visibilitychange', invalidate);
      window.removeEventListener('pagehide', invalidate); window.removeEventListener('pageshow', restore); };
  }, [invoke]);
  if (!state.visible) return null;
  const o = state.observation;
  const view = presentResident(o, state.pending, state.cleanupUnknown);
  const isOn = o?.standby === 'on';
  const isOff = o?.standby === 'off';
  const canSwitch = isOn ? state.canOff : isOff && state.canOn;
  const deadline = isOn && o.standbyUntil !== null ? new Date(o.standbyUntil) : null;
  const hasDeadline = deadline !== null && Number.isFinite(deadline.getTime());
  const extendRejected = state.lastRequestIntent === 'extend' && state.lastReceipt !== 'accepted' && !state.busy;
  return <Card id="resident-panel" aria-labelledby="resident-heading"
    data-resident-receipt={state.lastReceipt ?? 'none'} data-resident-request-id={state.lastRequestId}
    data-resident-registration={o?.registration ?? 'unknown'} data-resident-standby={o?.standby ?? 'unknown'}
    data-resident-transition-id={o?.transition?.requestId ?? 0} data-resident-transition-intent={o?.transition?.intent ?? 'none'}
    data-resident-transition-outcome={o?.transition?.outcome ?? 'none'}
    data-resident-revision={o?.revision ?? 0}>
    <p className="caption">カレントの検証版 / 常駐ホスト</p>
    <section className={`status${view.tone === 'neutral' ? '' : ` status--${view.tone}`}`} role="status" aria-live="polite" aria-atomic="true">
      <StatusContent view={view} icon={view.icon} headingId="resident-heading" descriptionId="resident-state" />
    </section>
    <section className="resident-standby" aria-labelledby="resident-standby-heading">
      <div className="resident-standby__row">
        <h3 id="resident-standby-heading">待機</h3>
        <Button id="resident-switch" className="resident-switch" variant={isOn ? 'danger' : 'default'}
          role={isOn || isOff ? 'switch' : undefined} aria-label={isOn || isOff ? '待機' : undefined}
          aria-checked={isOn || isOff ? isOn : undefined} aria-disabled={!canSwitch}
          onClick={() => {
            if (!canSwitch) return;
            if (isOn) void controller.current?.request('off');
            else void controller.current?.request('on', hours);
          }}>
          {(isOn || isOff) && <span className="resident-switch__track" aria-hidden="true" />}
          {isOn ? 'ON' : isOff ? 'OFF' : o?.standby === 'stopping' ? '停止中' : state.pending ? '確認中' : '未確認'}
        </Button>
      </div>
      {hasDeadline && <div className="resident-standby__row">
        <p>期限 <time dateTime={deadline.toISOString()}>
          {deadline.getMonth() + 1}月{deadline.getDate()}日 {deadline.getHours()}時{String(deadline.getMinutes()).padStart(2, '0')}分まで
        </time></p>
        <Button variant="quiet" aria-disabled={!state.canExtend}
          onClick={() => { if (state.canExtend) void controller.current?.request('extend', hours); }}>
          今から{hours}時間に{o?.standbyEndingSoon ? '延長' : '更新'}
        </Button>
      </div>}
      {hasDeadline && o?.standbyEndingSoon && <p className="resident-standby__notice" role="status">待機期限まで残り10分以内です。</p>}
      {hasDeadline && extendRejected && <p className="resident-standby__notice" role="status">期限を更新できませんでした。表示された期限を確認してください。</p>}
      <p className="caption">待機ONは作業の許可ではありません。</p>
      <details className="resident-standby__settings">
        <summary>詳細設定</summary>
        <div className="field">
          <label htmlFor="resident-hours">待機時間</label>
          <select id="resident-hours" className="input" value={hours} onChange={event => {
            const next = standbyHoursOptions.find(value => String(value) === event.currentTarget.value);
            if (next !== undefined) setHours(next);
          }}>
            {standbyHoursOptions.map(value => <option key={value} value={value}>{value}時間</option>)}
          </select>
        </div>
        <p className="caption">開始・更新した時点からの待機時間です。</p>
        <p className="caption">再接続しても切断前の作業は再開しません。再起動後は待機OFFから始まります。</p>
        <p className="caption">スリープ中も期限は進み、過ぎていれば復帰時に待機を停止します。</p>
      </details>
    </section>
    <p id="resident-diagnostic" className="shell-code">{state.diagnostic}</p>
  </Card>;
}
