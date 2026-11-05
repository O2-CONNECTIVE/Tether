// Diagnostic observer only. Preserve the request, response, parsed value and errors.
import fs from 'node:fs';
import path from 'node:path';

export function installCapture({ target, responseType, profile, output, filesystem }) {
  const originalFetch = target.fetch;
  const originalJson = responseType.prototype.json;
  const responses = new WeakSet();
  let requests = 0;
  let settlements = 0;
  let requestedMatchesEnvironment = false;
  let requestedAbsentBeforeRequest = false;
  let captureFailed = false;
  const normalize = value => {
    if (typeof value !== 'string' || !/^[A-Za-z]:[\\/].+/.test(value) || /[\x00\r\n"*?<>|]/.test(value) || value.slice(2).includes(':')) return null;
    const normalized = value.replaceAll('/', '\\');
    if (normalized.slice(3).split('\\').some(part => !part || part === '.' || part === '..' || /[ .]$/.test(part))) return null;
    return path.win32.normalize(normalized).toLowerCase();
  };
  const save = record => {
    try { filesystem.writeFileSync(path.join(output, 'profile-capability.json'), JSON.stringify(record)); }
    catch { captureFailed = true; }
  };
  target.fetch = async function (...args) {
    const isSession = args[0] === 'http://127.0.0.1:4444/session' && args[1]?.method === 'POST';
    if (isSession) {
      requests += 1;
      try {
        const requested = JSON.parse(args[1].body).capabilities.alwaysMatch['tauri:options'].webviewOptions.userDataFolder;
        requestedMatchesEnvironment = normalize(profile) !== null && normalize(requested) === normalize(profile);
        requestedAbsentBeforeRequest = !filesystem.existsSync(profile);
      } catch { captureFailed = true; }
      save({ schema: 1, requests, settlements, requestedMatchesEnvironment, requestedAbsentBeforeRequest,
        capabilityObserved: false, capabilityMatchesRequested: false, capabilityMapping: 'unavailable', captureFailed });
    }
    // No added timeout, retry, endpoint, argument substitution, or response-body read.
    const response = await Reflect.apply(originalFetch, this, args);
    if (isSession) { try { responses.add(response); } catch { captureFailed = true; } }
    return response;
  };
  responseType.prototype.json = async function (...args) {
    const value = await Reflect.apply(originalJson, this, args);
    if (responses.has(this)) {
      settlements += 1;
      let capabilityObserved = false;
      let capabilityMatchesRequested = false;
      let capabilityMapping = 'unavailable';
      try {
        const caps = value?.value?.capabilities;
        const flat = caps?.['msedge.userDataDir'];
        const nested = caps?.msedge?.userDataDir;
        // Two conflicting locations, or no documented value, are unknown.
        const present = [Object.hasOwn(caps ?? {}, 'msedge.userDataDir'),
          typeof caps?.msedge === 'object' && caps.msedge !== null && Object.hasOwn(caps.msedge, 'userDataDir')];
        const candidates = present.flatMap((exists, index) => exists ? [[flat, nested][index]] : []);
        capabilityObserved = candidates.length === 1 && typeof candidates[0] === 'string' && normalize(candidates[0]) !== null;
        capabilityMatchesRequested = capabilityObserved && normalize(profile) !== null && normalize(candidates[0]) === normalize(profile);
        if (capabilityObserved) capabilityMapping = capabilityMatchesRequested ? 'requested-root' :
          normalize(candidates[0]) === normalize(path.win32.join(profile, 'EBWebView')) ?
            'requested-child-ebwebview' : 'outside-requested-profile';
      } catch { captureFailed = true; }
      save({ schema: 1, requests, settlements, requestedMatchesEnvironment, requestedAbsentBeforeRequest,
        capabilityObserved, capabilityMatchesRequested, capabilityMapping, captureFailed });
    }
    return value;
  };
  return () => { target.fetch = originalFetch; responseType.prototype.json = originalJson; };
}

const profile = process.env.NATIVE_PROFILE;
const output = process.env.NATIVE_OUTPUT_DIR;
if (process.platform === 'win32' && profile && output) {
  installCapture({ target: globalThis, responseType: Response, profile, output, filesystem: fs });
}
