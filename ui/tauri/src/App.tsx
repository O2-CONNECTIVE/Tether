import { useCallback, useEffect, useRef, useState } from 'react';
import { ChevronRight, Square } from 'lucide-react';
import { createShellController, type ShellInvoke, type ShellState } from './controller.ts';
import { StatusContent } from './host-status/StatusContent.tsx';
import { presentWindowsObservation } from './host-status/windows-observation.ts';
import { Button } from './components/ui/button.tsx';
import { Card } from './components/ui/card.tsx';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from './components/ui/collapsible.tsx';

export function App({ invoke }: { invoke: ShellInvoke | undefined }) {
  const [state, setState] = useState<ShellState>({ kind: 'checking', diagnostic: 'CHECKING' });
  const controller = useRef<ReturnType<typeof createShellController> | null>(null);
  useEffect(() => {
    const instance = createShellController({ invoke, onChange: setState });
    controller.current = instance;
    void instance.refresh();
    const dispose = () => instance.dispose();
    window.addEventListener('pagehide', dispose, { once: true });
    return () => {
      instance.dispose();
      window.removeEventListener('pagehide', dispose);
      controller.current = null;
    };
  }, [invoke]);
  const refresh = useCallback(() => { void controller.current?.refresh(); }, []);
  // Fresh means this controller observation, not authenticated transport freshness.
  const { presentation } = presentWindowsObservation({
    scope: 'windows-shell-observation', version: 1, feed: 'fresh', state
  });
  const heading = state.kind === 'checking' ? '状態を確認中' : presentation.heading;
  const explanation = state.kind === 'checking' ? '接続機能の状態を取得しています。' : presentation.detail;
  return <main className="shell">
    <header><p className="caption">開発用 Tauri shell / 実接続機能なし</p><h1>サポートの状態</h1>
      <p>この shell はバックエンド未配線です。開始や停止はできません。</p></header>
    <section className={`status${state.kind === 'unavailable' ? ' status--unconfirmed' : ''}`} role="status" aria-live="polite" aria-atomic="true">
      <StatusContent headingId="status-title" descriptionId="status-description"
        icon={state.kind === 'checking' ? 'checking' : state.kind === 'unavailable' ? 'warning' : 'neutral'}
        view={{ heading, detail: state.diagnostic === 'BRIDGE_MISSING'
          ? 'ブラウザー表示では状態取得を利用できません。接続や通信終了は確認できません。' : explanation }} />
    </section>
    <Card role="region" aria-labelledby="permission-title"><h2 id="permission-title">許可条件</h2>
      <dl className="shell-permissions"><dt>許可相手</dt><dd>未確認</dd><dt>操作範囲</dt><dd>未確認</dd><dt>期限</dt><dd>未確認</dd><dt>通信の終了</dt><dd>未確認</dd></dl>
      <div className="shell-actions"><Button disabled aria-describedby="operation-help">サポートを開始</Button>
        <Button variant="danger" disabled aria-describedby="operation-help"><Square className="icon" aria-hidden="true" focusable="false" />接続を停止</Button></div>
      <p id="operation-help" className="caption">接続と停止の処理は未配線のため無効です。表示や残り時間を認可の根拠にはしません。</p>
    </Card>
    <Collapsible asChild><Card><CollapsibleTrigger asChild><Button variant="quiet" className="disclosure"><ChevronRight className="icon" aria-hidden="true" focusable="false" />診断情報</Button></CollapsibleTrigger>
      <CollapsibleContent className="diagnostics"><p>接続や終了の状態は確認できません。Rust IPC の取得状況だけを示します。</p>
        <p id="diagnostic" className="shell-code">{state.diagnostic}</p><Button id="refresh" aria-disabled={state.kind === 'checking'} aria-busy={state.kind === 'checking'} onClick={refresh}>状態を再確認</Button>
        <p className="caption">再確認は shell の状態を読み取るだけです。接続、停止、資格情報の保存は行いません。</p>
      </CollapsibleContent></Card></Collapsible>
    <footer className="caption">画面を閉じる操作は、通信が終了した証明にはなりません。認可、期限、失効の判断は、この画面の表示には依存しません。</footer>
  </main>;
}
