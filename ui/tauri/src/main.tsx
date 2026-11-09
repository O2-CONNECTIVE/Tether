import { createRoot } from 'react-dom/client';
import { App } from './App.tsx';
import type { ResidentInvoke } from './resident/model.ts';
import type { AppInvoke } from './app-controller.ts';
import '../design/tokens.css';
import '../design/components.css';
import './app.css';

declare global { interface Window { __TAURI__?: { core?: { invoke?: AppInvoke & ResidentInvoke } } } }
// The sole native IO adapter. UI and controller receive this dependency explicitly.
const nativeInvoke = window.__TAURI__?.core?.invoke;
const invoke: AppInvoke | undefined = typeof nativeInvoke === 'function'
  ? (command, args) => nativeInvoke(command, args) : undefined;
const root = document.getElementById('root');
if (!root) throw new Error('Missing application root');
const residentInvoke: ResidentInvoke | undefined = typeof nativeInvoke === 'function'
  ? (command, args) => nativeInvoke(command, args) : undefined;
createRoot(root).render(<App invoke={invoke} residentInvoke={residentInvoke} />);
