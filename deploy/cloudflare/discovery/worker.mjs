// Path-only pilot gate. Discovery-only additionally requires a fresh empty
// gateway with no host registration/grant. This wrapper authenticates nobody.
import native, { GATE_VALUE as NATIVE_GATE } from "../native/worker.mjs";
export const GATE_VALUE = "open-tether-discovery-only";
export const MAX_WINDOW_MS = 300_000;
const deny = () => new Response("not found", {status:404, headers:{"Cache-Control":"no-store"}});
const epoch = v => typeof v === "string" && /^[0-9]{13}$/.test(v) ? Number(v) : NaN;
export function discovery(worker, clock = Date.now) {
  return { async fetch(request, env, ctx) {
    const start = epoch(env?.TETHER_DISCOVERY_STARTS_AT);
    const end = epoch(env?.TETHER_DISCOVERY_EXPIRES_AT);
    const now = clock();
    if (env?.TETHER_DISCOVERY_GATE !== GATE_VALUE || !Number.isSafeInteger(start) ||
        !Number.isSafeInteger(end) || !Number.isFinite(now) || end <= start ||
        end - start > MAX_WINDOW_MS || now < start || now >= end) return deny();
    const url = new URL(request.url);
    // Registration, probe paths and all aliases stay closed, even when open.
    if (url.pathname !== "/mcp" || request.url.slice(url.origin.length) !== "/mcp") return deny();
    const response = await worker.fetch(request, {...env, TETHER_NATIVE_GATE:NATIVE_GATE}, ctx);
    // Do not release a late upstream reply beyond the absolute gate deadline.
    const after = clock();
    if (!Number.isFinite(after) || after < start || after >= end) {
      try { await response.body?.cancel(); } catch {}
      return deny();
    }
    return response;
  }};
}
export default discovery(native);
