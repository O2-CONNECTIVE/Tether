import { open, lstat } from 'node:fs/promises';
import path from 'node:path';
import { classifyAppStartup } from './app-startup-stages.ts';

type WryTrace = { schema: 1; variant: 'wry-startup-trace-1'; wryVersion: '0.57.1'; archiveSha256: string;
  patchSha256: string; preparedSourceSha256: string; generatedLockSha256: string; wrySourceSha256: string };
type StartupBuild = { profile: 'debug' | 'release'; variant: 'host-startup-diagnostic-1' | 'host-wry-startup-diagnostic-1'; wryTrace?: WryTrace };
function validWryTrace(input: unknown): input is WryTrace {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return false;
  const value = input as Record<string, unknown>;
  const hashes = ['archiveSha256', 'patchSha256', 'preparedSourceSha256', 'generatedLockSha256', 'wrySourceSha256'];
  const keys = ['schema', 'variant', 'wryVersion', ...hashes];
  return Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key))
    && value.schema === 1 && value.variant === 'wry-startup-trace-1' && value.wryVersion === '0.57.1'
    && value.archiveSha256 === '759c50ddad0ef9913bb5496cb9d509a50433a3b78cb0238d61105526589cf0e9'
    && hashes.every(key => typeof value[key] === 'string' && /^[0-9a-f]{64}$/.test(value[key]));
}
export function verifyStartupBuild(input: unknown, source: string, sha256: string): input is StartupBuild {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return false;
  const value = input as Record<string, unknown>;
  const wry = value.variant === 'host-wry-startup-diagnostic-1';
  const keys = ['schema', 'variant', 'sourceCommit', 'features', 'profile', 'exeSha256', 'distribution', ...(wry ? ['wryTrace'] : [])];
  return Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key))
    && value.schema === 1 && (wry || value.variant === 'host-startup-diagnostic-1') && value.distribution === false
    && (!wry || validWryTrace(value.wryTrace))
    && /^[0-9a-f]{40}$/.test(source) && /^[0-9a-f]{64}$/.test(sha256)
    && value.sourceCommit === source && value.exeSha256 === sha256
    && (value.profile === 'debug' || value.profile === 'release')
    && Array.isArray(value.features) && value.features.length === 2
    && value.features[0] === 'custom-protocol' && value.features[1] === 'startup-diagnostics';
}

export async function inspectAppStartupFile(directory: string, collection?: unknown) {
  let file: Awaited<ReturnType<typeof open>> | undefined;
  try {
    if (!collection || typeof collection !== 'object' || !('kind' in collection)
      || collection.kind !== 'windows-webview2-native-smoke' || !('appStartupFileClosed' in collection)
      || collection.appStartupFileClosed !== true) return classifyAppStartup(null);
    const filename = path.join(directory, 'app-startup-stages.json');
    const stat = await lstat(filename);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 2048) return classifyAppStartup(null);
    file = await open(filename, 'r');
    const buffer = Buffer.alloc(2049);
    const { bytesRead } = await file.read(buffer, 0, buffer.length, 0);
    if (bytesRead > 2048) return classifyAppStartup(null);
    const text = new TextDecoder('utf-8', { fatal: true }).decode(buffer.subarray(0, bytesRead));
    const value: unknown = JSON.parse(text);
    // Rustの固定compact出力だけを受け入れ、重複fieldや途中書込みを拒否する。
    return classifyAppStartup(JSON.stringify(value) === text ? value : null);
  } catch { return classifyAppStartup(null); }
  finally { await file?.close(); }
}
