import { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { HostStatusPanel } from './HostStatusPanel.tsx';
import { hostStatusFixtures } from './fixtures.ts';
import '../../design/tokens.css';
import '../../design/components.css';
import '../app.css';

function FixtureApp() {
  const [index, setIndex] = useState(0);
  const selected = hostStatusFixtures[index] ?? hostStatusFixtures[0];
  return <main className="shell">
    <header><p className="caption">表示テスト・実接続なし</p><h1>Windows Host 状態表示の見本</h1>
      <p>合成データ専用の独立入口です。Windows runtime・service・認証には接続していません。</p></header>
    <label htmlFor="sample">表示する見本 </label>
    <select id="sample" value={index} onChange={event => setIndex(Number(event.currentTarget.value))}>
      {hostStatusFixtures.map((fixture, value) => <option key={fixture.label} value={value}>{fixture.label}</option>)}
    </select>
    <HostStatusPanel sample={selected.input} />
    <footer><h2>表示と権限は別です</h2><p>この画面は承認・開始・停止・コマンド実行を行いません。本人の同意、grant、期限、回収は別の owner/runtime の責任です。</p>
      <p>現在の Windows Host の実動作や OS 隔離を証明するものではありません。</p></footer>
  </main>;
}
const root = document.getElementById('root');
if (!root) throw new Error('Missing fixture root');
createRoot(root).render(<FixtureApp />);
