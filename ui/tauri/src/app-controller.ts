import { createShellController, type ShellState, type ShellInvoke } from './controller.ts';
import { presentWindowsObservation } from './host-status/windows-observation.ts';

type View = { heading: string; detail: string; icon: 'checking' | 'neutral' | 'warning' };
export type AppState = { kind: ShellState['kind'] | 'observation'; diagnostic: string; view: View; synthetic: boolean; history?: View };
export type AppInvoke = (command: Parameters<ShellInvoke>[0] | 'get_status_presentation', args: Parameters<ShellInvoke>[1]) => ReturnType<ShellInvoke>;
type Route = { source: 'windows-shell' } | { source: 'unsupported' } | { source: 'mac-status'; state: 'source-unavailable' }
  | { source: 'mac-status'; state: 'synthetic-observation'; view: View; validForMs: number; cleanupUnknown: boolean; terminalHistory: boolean };
const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
const keys = (value: Record<string, unknown>, expected: string) => Object.keys(value).sort().join(',') === expected;
const text = (value: unknown): value is string => typeof value === 'string' && value.length > 0 && value.length <= 240 && !/[\p{Cc}]/u.test(value);
function route(value: unknown): Route | undefined {
  if (!record(value) || value.protocolVersion !== 1) return;
  if ((value.source === 'windows-shell' || value.source === 'unsupported') && keys(value, 'protocolVersion,source')) return { source: value.source };
  if (value.source !== 'mac-status') return;
  if (value.state === 'source-unavailable' && keys(value, 'protocolVersion,source,state')) return { source: value.source, state: value.state };
  if (value.state !== 'synthetic-observation' || !keys(value, 'cleanupUnknown,protocolVersion,source,state,terminalHistory,validForMs,view')) return;
  const view = value.view;
  if (!record(view) || !keys(view, 'detail,heading,icon') || !text(view.heading) || !text(view.detail)
    || (view.icon !== 'checking' && view.icon !== 'neutral' && view.icon !== 'warning')
    || typeof value.validForMs !== 'number' || !Number.isInteger(value.validForMs) || value.validForMs < 0 || value.validForMs > 3000
    || typeof value.cleanupUnknown !== 'boolean' || typeof value.terminalHistory !== 'boolean'
    || (value.cleanupUnknown && view.icon !== 'warning') || (value.terminalHistory && view.icon === 'checking')) return;
  return { source: value.source, state: value.state, view: { heading: view.heading, detail: view.detail, icon: view.icon },
    validForMs: value.validForMs, cleanupUnknown: value.cleanupUnknown, terminalHistory: value.terminalHistory };
}
function shellView(state: ShellState): AppState {
  const { presentation } = presentWindowsObservation({ scope: 'windows-shell-observation', version: 1, feed: 'fresh', state });
  return { ...state, synthetic: false, view: {
    heading: state.kind === 'checking' ? '状態を確認中' : presentation.heading,
    detail: state.kind === 'checking' ? '接続機能の状態を取得しています。' : state.diagnostic === 'BRIDGE_MISSING'
      ? 'ブラウザー表示では状態取得を利用できません。接続や通信終了は確認できません。' : presentation.detail,
    icon: state.kind === 'checking' ? 'checking' : state.kind === 'unavailable' ? 'warning' : 'neutral'
  } };
}
export const initialAppState = (): AppState => shellView({ kind: 'checking', diagnostic: 'CHECKING' });
const ended = Symbol('ended');
interface Options { invoke: AppInvoke | undefined; onChange: (state: AppState) => void; timeoutMs?: number; now?: () => number }
export function createAppController({ invoke, onChange, timeoutMs = 2000, now = () => performance.now() }: Options) {
  let disposed = false;
  let pending = false;
  let epoch = 0;
  let selected: Route['source'] | undefined;
  let observed = false;
  let invalidated = false;
  let cleanupUnknown = false;
  let history: View | undefined;
  let cancel: (() => void) | undefined;
  let displayTimer: ReturnType<typeof setTimeout> | undefined;
  const shell = createShellController({ invoke: invoke ? (command, args) => invoke(command, args) : undefined,
    onChange: state => { if (!disposed) onChange(shellView(state)); }, timeoutMs });
  const unknown = (diagnostic: string) => {
    if (disposed) return;
    onChange({ kind: 'unavailable', diagnostic, synthetic: observed, history, view: cleanupUnknown
      ? { heading: '回収を確認できません', detail: '操作が終わっていても、安全に回収されたとは限りません。', icon: 'warning' }
      : { heading: '状態の更新を確認できません', detail: '接続や停止は未確認です。古い報告から現在の状態を推定しないでください。', icon: 'warning' } });
  };
  const invalidate = () => {
    if (disposed || selected === 'windows-shell') return;
    epoch += 1;
    invalidated = true;
    cancel?.();
    clearTimeout(displayTimer);
    unknown(observed ? 'MAC_OBSERVATION_EXPIRED' : 'OBSERVATION_INVALIDATED');
  };
  return {
    async refresh() {
      if (disposed || pending) return;
      if (selected === 'windows-shell') { await shell.refresh(); return; }
      if (observed) { invalidate(); return; } // This temporary fixture bridge is single-shot.
      if (!invoke) { onChange(shellView({ kind: 'unavailable', diagnostic: 'BRIDGE_MISSING' })); return; }
      pending = true;
      const request = ++epoch;
      const started = now();
      onChange(initialAppState());
      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        const result = await Promise.race([invoke('get_status_presentation', { request: { protocolVersion: 1 } }),
          new Promise(resolve => { timer = setTimeout(() => { invalidated = true; resolve(ended); }, timeoutMs); }),
          new Promise(resolve => { cancel = () => resolve(ended); })]);
        if (disposed || request !== epoch) return;
        if (result === ended) { unknown('IPC_TIMEOUT'); return; }
        const value = route(result);
        if (!value || (selected && value.source !== selected)) { invalidated = true; unknown('INVALID_RESPONSE'); return; }
        selected = value.source;
        if (value.source === 'windows-shell') { await shell.refresh(); return; }
        if (value.source === 'unsupported') { unknown('PLATFORM_UNSUPPORTED'); return; }
        if (value.state === 'source-unavailable') {
          onChange({ kind: 'unavailable', diagnostic: 'MAC_SOURCE_UNAVAILABLE', synthetic: false, view: {
            heading: 'Mac の状態取得は未接続です', detail: '状態を受け取る native 入力はまだ接続されていません。接続や停止は確認できません。', icon: 'warning'
          } });
          return;
        }
        const elapsed = now() - started;
        // A busy event loop can resolve IPC before its already-due timer runs.
        if (Number.isFinite(elapsed) && elapsed >= timeoutMs) { invalidated = true; unknown('IPC_TIMEOUT'); return; }
        observed = true;
        cleanupUnknown ||= value.cleanupUnknown;
        if (value.terminalHistory) history = value.view;
        const budget = Math.floor(value.validForMs - elapsed);
        if (invalidated || !Number.isFinite(elapsed) || elapsed < 0 || budget <= 0) { invalidate(); return; }
        onChange({ kind: 'observation', diagnostic: 'MAC_SYNTHETIC_OBSERVATION', synthetic: true, view: value.view, history });
        displayTimer = setTimeout(invalidate, budget);
      } catch {
        if (!disposed && request === epoch) { invalidated = true; unknown('IPC_ERROR'); }
      } finally {
        clearTimeout(timer);
        pending = false;
        cancel = undefined;
      }
    },
    invalidate,
    dispose() {
      if (disposed) return;
      disposed = true;
      epoch += 1;
      cancel?.();
      clearTimeout(displayTimer);
      shell.dispose();
    }
  };
}
