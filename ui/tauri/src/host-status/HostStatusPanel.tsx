import { StatusContent } from './StatusContent.tsx';
import { presentHostStatus } from './presentation.ts';

export function HostStatusPanel({ sample }: { sample: unknown }) {
  const view = presentHostStatus(sample);
  return <section aria-label="合成ホスト状態" role="status" aria-live="polite" aria-atomic="true">
    <div className="status"><StatusContent view={view} icon={view.tone === 'warning' ? 'warning' : 'neutral'} /></div>
    <dl className="shell-permissions"><dt>承認の残り時間（表示見本）</dt><dd>{view.remaining}</dd>
      <dt>管理中の操作数</dt><dd>{view.workers}</dd><dt>停止と回収</dt><dd>{view.cleanup}</dd></dl>
    <p>件数は OS 全体のプロセス数ではありません。0 秒・EOF・画面を閉じる操作は回収確認ではありません。</p>
  </section>;
}
