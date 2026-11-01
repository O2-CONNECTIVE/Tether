// Tether commandless route probe: Cloudflare Worker source candidate.
//
// DENY ALL unless env.TETHER_PROBE_GATE equals GATE_VALUE exactly. Even then
// only POST HTTP_PATH and a GET WebSocket upgrade at WS_PATH are forwarded,
// to fixed upstream paths, through the ONE VPC Service binding
// env.TETHER_PROBE_VPC. There is no global fetch, no request-selected target
// and no redirect. This Worker authenticates nobody: the Go tether-echo-probe
// verifies the Access assertion and the subject allowlist. Nothing is logged.

export const HTTP_PATH = "/tether-probe/v1/echo";
export const WS_PATH = "/tether-probe/v1/ws";
export const SUBPROTOCOL = "tether-probe.v1";
export const ASSERTION_HEADER = "Cf-Access-Jwt-Assertion";
export const GATE_VALUE = "open-commandless-echo-probe";
// URL for binding.fetch. The VPC Service sends to its own fixed host and port
// (confirmed by the coordinator from the official documentation), so this
// supplies the path; the Go probe does not read Host.
export const UPSTREAM_ORIGIN = "http://tether-echo-probe.internal";

// The Go probe closes a WebSocket at 30 s; the Worker timer is a backstop.
// bodyMs bounds reading the client's request body.
export const LIMITS = Object.freeze({
  assertion: 8192, body: 1024, reply: 4096, message: 1024, frames: 32, bodyMs: 5_000, upstreamMs: 10_000, lifetimeMs: 35_000,
});

const TIMED_OUT = Symbol("timed out");
const PLAIN = { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" };

export function createProbeWorker(rt) {
  return {
    async fetch(request, env) {
      try {
        return await route(request, env, rt);
      } catch {
        return deny(rt, 502, "probe upstream failed");
      }
    },
  };
}

export default createProbeWorker({
  Response: globalThis.Response,
  newPair: () => {
    const pair = new WebSocketPair();
    return [pair[0], pair[1]];
  },
  setTimeout: (f, ms) => setTimeout(f, ms),
  clearTimeout: (t) => clearTimeout(t),
});

async function route(request, env, rt) {
  if (!env || env.TETHER_PROBE_GATE !== GATE_VALUE) return deny(rt, 404, "not found");
  const binding = env.TETHER_PROBE_VPC;
  if (!binding || typeof binding.fetch !== "function") return deny(rt, 503, "probe route not configured");
  const url = new URL(request.url);
  if (url.pathname !== HTTP_PATH && url.pathname !== WS_PATH) return deny(rt, 404, "not found");
  if (url.search !== "" || request.url.includes("?")) return deny(rt, 400, "query parameters are not accepted");
  // Reject observable raw-path aliases before normalized URL routing.
  if (request.url.slice(url.origin.length) !== url.pathname) return deny(rt, 404, "not found");
  const upgrade = (request.headers.get("Upgrade") ?? "").toLowerCase();
  const echo = url.pathname === HTTP_PATH;
  if (echo ? request.method !== "POST" || upgrade !== "" : request.method !== "GET" || upgrade !== "websocket") {
    return deny(rt, 405, "method not allowed", { Allow: echo ? "POST" : "GET" });
  }
  // The Go probe refuses any Origin, and Origin is never forwarded, so it is
  // refused here instead of being silently dropped.
  if (request.headers.has("Origin")) return deny(rt, 403, "origin not allowed");
  // Presence is an early refusal only, never authentication: the backend
  // verifies the signature, issuer, AUD, lifetime and subject.
  const assertion = request.headers.get(ASSERTION_HEADER);
  if (!assertion || assertion.length > LIMITS.assertion || assertion.includes(",")) {
    return deny(rt, 403, "exactly one Cloudflare Access assertion is required");
  }
  return echo ? forwardEcho(request, assertion, binding, rt) : forwardSocket(request, assertion, binding, rt);
}

async function forwardEcho(request, assertion, binding, rt) {
  if (request.headers.get("Content-Type") !== "application/octet-stream") {
    return deny(rt, 415, "Content-Type must be application/octet-stream");
  }
  const declared = request.headers.get("Content-Length");
  if (declared !== null && !(/^\d{1,5}$/.test(declared) && Number(declared) <= LIMITS.body)) {
    return deny(rt, 413, "probe body exceeds the size limit");
  }
  const body = await withDeadline(rt, LIMITS.bodyMs, (signal) => readBounded(request.body, LIMITS.body, signal));
  if (body === TIMED_OUT) return deny(rt, 408, "probe body read timed out");
  if (body === null) return deny(rt, 413, "probe body exceeds the size limit");
  const result = await withDeadline(rt, LIMITS.upstreamMs, async (signal) => {
    const response = await binding.fetch(UPSTREAM_ORIGIN + HTTP_PATH, {
      method: "POST", headers: { [ASSERTION_HEADER]: assertion, "Content-Type": "application/octet-stream" },
      body, redirect: "manual", signal,
    });
    // A response that arrives after the deadline (the binding may ignore the
    // abort) is cancelled, never read.
    if (signal.aborted || response.status < 200 || (response.status >= 300 && response.status < 400) || response.status > 599) {
      await cancelBody(response);
      return null;
    }
    const reply = await readBounded(response.body, LIMITS.reply, signal);
    return reply === null ? null : { status: response.status, json: response.headers.get("Content-Type") === "application/json", reply };
  });
  if (result === TIMED_OUT) return deny(rt, 504, "probe upstream timed out");
  if (result === null) return deny(rt, 502, "probe upstream reply refused");
  return new rt.Response(result.reply, {
    status: result.status, headers: { ...PLAIN, "Content-Type": result.json ? "application/json" : PLAIN["Content-Type"] },
  });
}

async function forwardSocket(request, assertion, binding, rt) {
  if (request.headers.get("Sec-WebSocket-Protocol") !== SUBPROTOCOL) {
    return deny(rt, 400, "the fixed probe subprotocol is required");
  }
  const response = await withDeadline(rt, LIMITS.upstreamMs, async (signal) => {
    const answer = await binding.fetch(UPSTREAM_ORIGIN + WS_PATH, {
      method: "GET", headers: { Upgrade: "websocket", [ASSERTION_HEADER]: assertion, "Sec-WebSocket-Protocol": SUBPROTOCOL },
      redirect: "manual", signal,
    });
    // An upgrade that arrives after the deadline is closed, never relayed.
    if (signal.aborted) {
      await discard(answer, 1011, "probe upstream timed out");
      return null;
    }
    return answer;
  });
  if (response === TIMED_OUT || response === null) return deny(rt, 504, "probe upstream timed out");
  const upstream = response.webSocket;
  if (response.status !== 101 || !upstream) {
    await cancelBody(response);
    const status = [400, 403, 503].includes(response.status) ? response.status : 502;
    return deny(rt, status, "probe upstream refused the WebSocket");
  }
  if (response.headers.get("Sec-WebSocket-Protocol") !== SUBPROTOCOL) {
    await discard(response, 1002, "probe subprotocol mismatch");
    return deny(rt, 502, "probe upstream subprotocol mismatch");
  }
  const [client, server] = rt.newPair();
  relay(server, upstream, rt);
  return new rt.Response(null, { status: 101, webSocket: client, headers: { "Sec-WebSocket-Protocol": SUBPROTOCOL } });
}

// relay joins the client-facing socket and the upstream socket. Each
// direction forwards at most LIMITS.frames messages of at most LIMITS.message
// bytes, keeping text as text and binary as binary. One timer bounds the
// whole relay; the first close, error or limit closes both ends once and
// clears it.
export function relay(client, upstream, rt, limits = LIMITS) {
  let open = true;
  const timer = rt.setTimeout(() => end(4000, "probe lifetime ended"), limits.lifetimeMs);
  function end(code, reason) {
    if (!open) return;
    open = false;
    rt.clearTimeout(timer);
    closeQuietly(client, code, reason);
    closeQuietly(upstream, code, reason);
  }
  function pipe(from, to) {
    let frames = 0;
    from.addEventListener("message", (event) => {
      if (!open) return;
      const size = messageSize(event.data);
      if (size < 0 || size > limits.message) return end(1009, "probe message too big");
      if (++frames > limits.frames) return end(1008, "probe frame budget reached");
      try {
        to.send(event.data);
      } catch {
        end(1011, "probe relay failed");
      }
    });
    from.addEventListener("close", (event) => end(closeCode(event.code), closeReason(event.reason)));
    from.addEventListener("error", () => end(1011, "probe relay failed"));
  }
  // Workers compatibility dates >= 2026-03-17 default binary delivery to Blob.
  // Keep the bounded synchronous relay representation explicit on both legs.
  client.binaryType = "arraybuffer";
  upstream.binaryType = "arraybuffer";
  client.accept();
  upstream.accept();
  pipe(client, upstream);
  pipe(upstream, client);
}

const encoder = new TextEncoder();

function messageSize(data) {
  if (typeof data === "string") return encoder.encode(data).byteLength;
  if (data instanceof ArrayBuffer || ArrayBuffer.isView(data)) return data.byteLength;
  return -1;
}

// Sendable close codes pass through; 1005 (no status) becomes 1000 and
// anything else, such as 1006 (abnormal), becomes 1011.
export function closeCode(code) {
  if (code === 1000 || [1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011].includes(code) || (code >= 3000 && code <= 4999)) return code;
  return code === 1005 ? 1000 : 1011;
}

function closeReason(reason) {
  return typeof reason === "string" && encoder.encode(reason).byteLength <= 123 ? reason : "";
}

function closeQuietly(ws, code, reason) {
  try {
    ws.close(code, reason);
  } catch {
    // already closing or closed
  }
}

async function cancelBody(response) {
  try {
    await response.body?.cancel();
  } catch {
    // nothing left to release
  }
}

// discard releases an unwanted upstream answer: an upgraded socket is accepted
// only to be closed; any other response has its body cancelled.
async function discard(response, code, reason) {
  const ws = response.webSocket;
  if (!ws) return cancelBody(response);
  try {
    ws.accept();
  } catch {
    // already accepted or closed
  }
  closeQuietly(ws, code, reason);
}

// withDeadline runs work under one timer that aborts it; the timer is always
// cleared. The deadline settles before the abort so that the abort's own
// rejection cannot win the race. A late rejection of abandoned work is
// absorbed.
async function withDeadline(rt, ms, work) {
  const controller = new AbortController();
  let timer;
  const expired = new Promise((resolve) => {
    timer = rt.setTimeout(() => {
      resolve(TIMED_OUT);
      controller.abort();
    }, ms);
  });
  const pending = Promise.resolve().then(() => work(controller.signal));
  pending.catch(() => {});
  try {
    return await Promise.race([pending, expired]);
  } finally {
    rt.clearTimeout(timer);
  }
}

// readBounded returns the bytes of stream, or null once more than max arrive
// or signal aborts. An abort cancels the reader itself, which settles a
// pending read; every return or throw short of the end also cancels it.
async function readBounded(stream, max, signal) {
  if (stream === null || stream === undefined) return new Uint8Array(0);
  const reader = stream.getReader();
  const cancel = () => {
    reader.cancel().catch(() => {});
  };
  signal.addEventListener("abort", cancel);
  let complete = false;
  try {
    const chunks = [];
    let size = 0;
    for (;;) {
      if (signal.aborted) return null;
      const { done, value } = await reader.read();
      if (signal.aborted) return null;
      if (done) break;
      if (!(value instanceof Uint8Array)) throw new TypeError("unexpected body chunk");
      size += value.byteLength;
      if (size > max) return null;
      chunks.push(value);
    }
    complete = true;
    const out = new Uint8Array(size);
    let at = 0;
    for (const chunk of chunks) {
      out.set(chunk, at);
      at += chunk.byteLength;
    }
    return out;
  } finally {
    signal.removeEventListener("abort", cancel);
    if (!complete) cancel();
  }
}

function deny(rt, status, message, extra = {}) {
  return new rt.Response(message + "\n", { status, headers: { ...PLAIN, ...extra } });
}
