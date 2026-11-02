import { CircleAlert, CircleDashed, LoaderCircle } from 'lucide-react';
import type { HostStatusPresentation } from './presentation.ts';

interface Props {
  view: Pick<HostStatusPresentation, 'heading' | 'detail'>;
  icon: 'checking' | 'neutral' | 'warning';
  headingId?: string;
  descriptionId?: string;
}
// Read-only presentation output only. Callers own the live region and all input,
// authority, freshness and lifecycle decisions; this component performs no IO.
export function StatusContent({ view, icon, headingId, descriptionId }: Props) {
  const Icon = icon === 'checking' ? LoaderCircle : icon === 'warning' ? CircleAlert : CircleDashed;
  return <>
    <span className={`status__icon${icon === 'checking' ? ' icon-spin' : ''}`}><Icon className="icon" aria-hidden="true" focusable="false" /></span>
    <div className="status__content"><h2 id={headingId}>{view.heading}</h2><p id={descriptionId}>{view.detail}</p></div>
  </>;
}
