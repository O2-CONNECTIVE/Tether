import { useEffect, useRef, useState } from 'react';
import { Button } from '../components/ui/button.tsx';
import { Card } from '../components/ui/card.tsx';
import { createResidentController, initialResidentState } from './controller.ts';
import type { ResidentInvoke } from './model.ts';
import { presentResident } from './presentation.ts';
import { StatusContent } from '../host-status/StatusContent.tsx';
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
  const view = presentResident(o, state.pending);
  return <Card id="resident-panel" role="region" aria-labelledby="resident-heading"
    data-resident-receipt={state.lastReceipt ?? 'none'} data-resident-request-id={state.lastRequestId}
    data-resident-registration={o?.registration ?? 'unknown'} data-resident-standby={o?.standby ?? 'unknown'}
    data-resident-transition-id={o?.transition?.requestId ?? 0} data-resident-transition-intent={o?.transition?.intent ?? 'none'}
    data-resident-transition-outcome={o?.transition?.outcome ?? 'none'}
    data-resident-revision={o?.revision ?? 0}>
    <p className="caption">カレントの検証版 / 常駐ホスト</p>
    <section className={`status${view.tone === 'neutral' ? '' : ` status--${view.tone}`}`} role="status" aria-live="polite" aria-atomic="true">
      <StatusContent view={view} icon={view.icon} headingId="resident-heading" descriptionId="resident-state" />
    </section>
    <div className="shell-actions"><Button id="resident-on" disabled={!state.canOn} onClick={() => { void controller.current?.request('on'); }}>待機をON</Button>
      <Button id="resident-off" variant="danger" disabled={!state.canOff} onClick={() => { void controller.current?.request('off'); }}>待機をOFF</Button></div>
    <p className="caption">受付は変更完了ではありません。待機ONは作業の許可ではなく、OFF・画面閉鎖は全回収の証明ではありません。</p>
    <p id="resident-diagnostic" className="shell-code">{state.diagnostic}</p>
  </Card>;
}
