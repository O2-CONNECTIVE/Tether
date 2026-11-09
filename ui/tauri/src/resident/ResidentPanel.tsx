import { useEffect, useRef, useState } from 'react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import { createResidentController, initialResidentState } from './controller.ts';
import type { ResidentInvoke } from './model.ts';
export function ResidentPanel({ invoke }: { invoke: ResidentInvoke | undefined }) {
  const [state, setState] = useState(initialResidentState);
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
  const heading = !o ? '常駐ホストの状態を確認できません' : state.pending ? '待機の変更を確認中'
    : o.standby === 'on' ? '待機ONを確認' : o.standby === 'off' ? '待機OFFを確認' : '待機停止を確認中';
  return <Card id="resident-panel" role="region" aria-labelledby="resident-heading"
    data-resident-receipt={state.lastReceipt ?? 'none'} data-resident-request-id={state.lastRequestId}
    data-resident-registration={o?.registration ?? 'unknown'} data-resident-standby={o?.standby ?? 'unknown'}
    data-resident-transition-id={o?.transition?.requestId ?? 0} data-resident-transition-intent={o?.transition?.intent ?? 'none'}
    data-resident-transition-outcome={o?.transition?.outcome ?? 'none'}
    data-resident-revision={o?.revision ?? 0}>
    <p className="caption">カレントの検証版 / 常駐ホスト</p>
    <div role="status" aria-live="polite" aria-atomic="true"><h2 id="resident-heading">{heading}</h2>
      <p id="resident-state">{o ? `待機: ${o.standby} / 接続: ${o.registration} / 作業: ${o.work} / 回収: ${o.cleanup}` : '未確認。接続・停止・回収の成功を示しません。'}</p></div>
    <div className="shell-actions"><Button id="resident-on" disabled={!state.canOn} onClick={() => { void controller.current?.request('on'); }}>待機をON</Button>
      <Button id="resident-off" variant="danger" disabled={!state.canOff} onClick={() => { void controller.current?.request('off'); }}>待機をOFF</Button></div>
    <p className="caption">受付は変更完了ではありません。待機ONは作業の許可ではなく、OFF・画面閉鎖は全回収の証明ではありません。</p>
    <p id="resident-diagnostic" className="shell-code">{state.diagnostic}</p>
  </Card>;
}
