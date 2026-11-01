// Source-only bounded pilot entry. No authority, keys, logs or deployment.
// Reuses the native protocol; temporal bounds only narrow its existing gate.
import { createNativeWorker, GATE_VALUE } from "./worker.mjs";
export const MAX_WINDOW_MS = 300_000;
const MAX_ACTIVE = 32;

function windowOf(env) {
  const a = env?.TETHER_NATIVE_STARTS, b = env?.TETHER_NATIVE_EXPIRES;
  if (typeof a !== "string" || typeof b !== "string" ||
      !/^[0-9]{13}$/.test(a) || !/^[0-9]{13}$/.test(b)) return null;
  const start = Number(a), end = Number(b);
  return end > start && end - start <= MAX_WINDOW_MS ? { start, end } : null;
}

export function createBoundedPilot(rt) {
  let fixed = null, last = null, poisoned = false;
  const active = new Set();
  const denied = () => new rt.Response("not found\n", {
    status: 404, headers: { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" },
  });
  function poison() {
    poisoned = true;
    for (const scope of [...active]) scope.expire();
    return false;
  }
  function valid() {
    let now;
    try { now = rt.now(); } catch { return poison(); }
    if (!Number.isSafeInteger(now) || now < 0 || (last !== null && now < last)) return poison();
    last = now;
    if (poisoned || !fixed || now < fixed.start || now >= fixed.end) return poison();
    return true;
  }
  function remaining() { return Math.max(0, fixed.end - last); }

  return {
    async fetch(request, env) {
      const proposed = windowOf(env);
      if (!proposed || env?.TETHER_NATIVE_GATE !== GATE_VALUE) {
        // Closing or corrupting a configured window closes its owned relays.
        if (fixed) poison();
        return denied();
      }
      if (fixed && (proposed.start !== fixed.start || proposed.end !== fixed.end)) {
        poison(); return denied();
      }
      if (!fixed) {
        // Early requests do not consume a future valid window.
        let now;
        try { now = rt.now(); } catch { return denied(); }
        if (!Number.isSafeInteger(now) || now < proposed.start || now >= proposed.end) return denied();
        fixed = proposed;
      }
      if (!valid()) return denied();
      if (active.size >= MAX_ACTIVE) return new rt.Response("pilot busy\n", { status: 503 });
      let closed = false, expiry;
      const timers = new Map(), sockets = new Set();
      const scope = {
        expire() {
          if (closed) return;
          closed = true;
          rt.clearTimeout(expiry);
          // Settle native deadlines and abort their owned I/O before releasing.
          const pending = [...timers.values()];
          timers.clear();
          for (const entry of pending) {
            rt.clearTimeout(entry.id);
            try { entry.fn(); } catch {}
          }
          for (const socket of sockets) {
            try { socket.close(4000, "pilot window ended"); } catch {}
          }
          active.delete(scope);
        },
        release() {
          rt.clearTimeout(expiry);
          active.delete(scope);
        },
      };
      active.add(scope);
      expiry = rt.setTimeout(() => poison(), remaining());
      const alive = () => !closed && valid();
      function wrap(socket) {
        sockets.add(socket);
        return {
          get binaryType() { return socket.binaryType; },
          set binaryType(value) { socket.binaryType = value; },
          accept() { if (!alive()) throw new Error("pilot closed"); socket.accept(); },
          send(value) { if (!alive()) throw new Error("pilot closed"); socket.send(value); },
          close(code, reason) { socket.close(code, reason); },
          addEventListener(type, fn) {
            socket.addEventListener(type, event => {
              if (type === "message" && !alive()) return;
              fn(event);
            });
          },
        };
      }
      const scoped = {
        Response: rt.Response,
        newPair() {
          if (!alive()) throw new Error("pilot closed");
          const [client, server] = rt.newPair();
          return [client, wrap(server)];
        },
        setTimeout(fn, ms) {
          const delay = alive() && Number.isFinite(ms) && ms >= 0 ? Math.min(ms, remaining()) : 0;
          const entry = { fn, id: null };
          entry.id = rt.setTimeout(() => {
            timers.delete(entry.id);
            if (!alive()) scope.expire();
            fn();
          }, delay);
          timers.set(entry.id, entry);
          return entry.id;
        },
        clearTimeout(id) { timers.delete(id); rt.clearTimeout(id); },
      };
      const binding = env.TETHER_NATIVE_VPC;
      const guardedBinding = {
        async fetch(url, init) {
          if (!alive() || !binding || typeof binding.fetch !== "function") throw new Error("pilot closed");
          const response = await binding.fetch(url, init);
          if (!alive()) { await discard(response); throw new Error("pilot closed"); }
          if (response.webSocket) return {
            status: response.status, headers: response.headers, body: response.body,
            webSocket: wrap(response.webSocket),
          };
          return response;
        },
      };
      let response;
      try {
        response = await createNativeWorker(scoped).fetch(request, {
          ...env, TETHER_NATIVE_VPC: guardedBinding,
        });
        if (!alive()) { await discard(response); scope.expire(); return denied(); }
        if (response.status !== 101) scope.release();
        return response;
      } catch {
        scope.expire();
        return denied();
      }
    },
  };
}

// Releasing a late upgraded response never starts a relay. HTTP body release
// does not wait on an untrusted cancel promise.
async function discard(response) {
  if (response?.webSocket) {
    try { response.webSocket.accept(); } catch {}
    try { response.webSocket.close(4000, "pilot window ended"); } catch {}
  } else {
    try { Promise.resolve(response?.body?.cancel()).catch(() => {}); } catch {}
  }
}

export default createBoundedPilot({
  now: () => Date.now(),
  Response: globalThis.Response,
  newPair: () => { const p = new WebSocketPair(); return [p[0], p[1]]; },
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: id => clearTimeout(id),
});
