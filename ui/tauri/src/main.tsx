import { createRoot } from 'react-dom/client';
import { App } from './App.tsx';
import type { PairingInvoke, ResidentInvoke } from './resident/model.ts';
import type { AppInvoke } from './app-controller.ts';
import '../design/tokens.css';
import '../design/components.css';
import './app.css';

type NativeResidentInvoke = ResidentInvoke;
declare global { interface Window { __TAURI__?: { core?: { invoke?: AppInvoke & NativeResidentInvoke } } } }
// The sole native IO adapter. UI and controller receive this dependency explicitly.
const nativeInvoke = window.__TAURI__?.core?.invoke;
const callNative = nativeInvoke as unknown as (command: string, args?: unknown) => Promise<unknown>;
const invoke: AppInvoke | undefined = typeof nativeInvoke === 'function'
  ? (command, args) => nativeInvoke(command, args) : undefined;
const root = document.getElementById('root');
if (!root) throw new Error('Missing application root');
const residentInvoke: ResidentInvoke | undefined = typeof nativeInvoke === 'function'
  ? (command, args) => callNative(command, command === 'request_resident_intent'
    ? { request: JSON.stringify(args.request) } : args) : undefined;
const pairingInvoke: PairingInvoke | undefined = typeof nativeInvoke === 'function'
  ? () => callNative('request_resident_pairing') : undefined;
createRoot(root).render(<App invoke={invoke} residentInvoke={residentInvoke} pairingInvoke={pairingInvoke} />);
