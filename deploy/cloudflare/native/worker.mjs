// Tether native route: Cloudflare Worker source candidate for the existing Go
// rescue-gateway.
//
// DENY ALL unless env.TETHER_NATIVE_GATE equals GATE_VALUE exactly. Even then
// only POST/DELETE MCP_PATH and a GET WebSocket upgrade at REGISTER_PATH are
// forwarded, to fixed upstream paths, through the ONE VPC Service binding
// env.TETHER_NATIVE_VPC. There is no global fetch, no request-selected target
// and no redirect. This Worker authenticates nobody and issues no grant: for
// MCP the Go agentgateway verifies the Access assertion and maps its subject;
// for registration the Go agenttransport verifies the enrolled host-key proof
// and the agent channel checks the same grant before any command. Nothing is
// logged.

// The WebSocket relay is the reviewed probe relay (explicit ArrayBuffer
// binaryType, one close, one lifetime timer); only the limits differ.
import { relay } from "../probe/worker.mjs";

export const MCP_PATH = "/mcp";
export const REGISTER_PATH = "/tether-agent/v1/register";
export const SUBPROTOCOL = "rescue-agent-register.v1";
export const RESIDENT_SUBPROTOCOL = "rescue-agent-resident.v1";
export const RESIDENT_GATE_VALUE = "open-resident-tether-control";
export const ASSERTION_HEADER = "Cf-Access-Jwt-Assertion";
export const GATE_VALUE = "open-native-tether-gateway";
// URL for binding.fetch. The VPC Service sends to its own fixed host and port,
// so this supplies the path only; the Go gateway routes by exact path.
export const UPSTREAM_ORIGIN = "http://rescue-gateway.internal";

// body and reply match mcpadapter.MaxLine (512 KiB); the Go call timeout is
// 30 s, so upstreamMs bounds one MCP round trip including the reply body.
// message is agenttransport.MaxStreamMessage (registration text is 2 KiB and
// enforced by Go). lifetimeMs is the absolute pilot relay lifetime; nothing
// reconnects or extends it.
export const LIMITS = Object.freeze({
  assertion: 8192, body: 512 * 1024, reply: 512 * 1024, bodyMs: 10_000, upstreamMs: 35_000, upgradeMs: 10_000,
  message: 32 * 1024, frames: 4096, lifetimeMs: 300_000,
});

// Request headers forwarded to MCP, per method, with their length bound. Every
// other header (Authorization, Cookie, identity or forwarding headers) is
// dropped. The Go gateway still validates each value.
const MCP_HEADERS = {
  POST: [["Content-Type", 128], ["Accept", 256], ["Mcp-Session-Id", 128], ["Mcp-Protocol-Version", 32]],
  DELETE: [["Mcp-Session-Id", 128], ["Mcp-Protocol-Version", 32]],
};
// Statuses the Go gateway answers in Access mode. Anything else, including a
// redirect or 401, becomes 502.
const MCP_STATUSES = new Set([200, 202, 204, 400, 403, 404, 405, 406, 413, 415, 429, 500, 503]);
const MCP_TYPES = ["application/json", "text/plain; charset=utf-8"];
// Response headers relayed when well-formed. Location, Set-Cookie,
// WWW-Authenticate and every other header are never relayed.
const MCP_REPLY_HEADERS = [
  ["Mcp-Session-Id", /^[0-9a-f]{32}$/], ["Mcp-Protocol-Version", /^\d{4}-\d{2}-\d{2}$/],
  ["Retry-After", /^\d{1,4}$/], ["Allow", /^POST, DELETE$/],
];
const REGISTER_REFUSALS = [400, 403, 404, 405, 503];
// Registration is outside human OAuth: no ambient credential and no Access
// assertion is accepted or forwarded.
const REGISTER_FORBIDDEN = ["Cookie", "Authorization", "Proxy-Authorization", ASSERTION_HEADER];

const TIMED_OUT = Symbol("timed out");
const SAFE = { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" };
const PLAIN = { "Content-Type": "text/plain; charset=utf-8", ...SAFE };
const VISIBLE = /^[\x20-\x7e]+$/;

export function createNativeWorker(rt) {
  return {
    async fetch(request, env) {
      try {
        return await route(request, env, rt);
      } catch {
        return deny(rt, 502, "native upstream failed");
      }
    },
  };
}

export default createNativeWorker({
  Response: globalThis.Response,
  newPair: () => {
    const pair = new WebSocketPair();
    return [pair[0], pair[1]];
  },
  setTimeout: (f, ms) => setTimeout(f, ms),
  clearTimeout: (t) => clearTimeout(t),
});

async function route(request, env, rt) {
  if (!env || env.TETHER_NATIVE_GATE !== GATE_VALUE) return deny(rt, 404, "not found");
  const binding = env.TETHER_NATIVE_VPC;
  if (!binding || typeof binding.fetch !== "function") return deny(rt, 503, "native route not configured");
  const url = new URL(request.url);
  if (url.pathname !== MCP_PATH && url.pathname !== REGISTER_PATH) return deny(rt, 404, "not found");
  if (url.search !== "" || request.url.includes("?")) return deny(rt, 400, "query parameters are not accepted");
  // Reject observable raw-path aliases before normalized URL routing.
  if (request.url.slice(url.origin.length) !== url.pathname) return deny(rt, 404, "not found");
  const mcp = url.pathname === MCP_PATH;
  if (mcp) {
    // No GET stream is offered and no upgrade or CORS preflight is answered.
    if ((request.method !== "POST" && request.method !== "DELETE") || request.headers.has("Upgrade")) {
      return deny(rt, 405, "method not allowed", { Allow: "POST, DELETE" });
    }
  } else if (request.method !== "GET" || (request.headers.get("Upgrade") ?? "").toLowerCase() !== "websocket") {
    return deny(rt, 405, "method not allowed", { Allow: "GET" });
  }
  // No Origin is configured for either Go handler, so it is refused here
  // rather than dropped.
  if (request.headers.has("Origin")) return deny(rt, 403, "origin not allowed");
  return mcp ? forwardMcp(request, binding, rt) : forwardRegister(request, binding, rt, env.TETHER_RESIDENT_GATE === RESIDENT_GATE_VALUE);
}

async function forwardMcp(request, binding, rt) {
  // Presence is an early refusal only, never authentication: the Go gateway
  // verifies the signature, issuer, AUD, lifetime and maps the subject.
  const assertion = request.headers.get(ASSERTION_HEADER);
  if (!assertion || assertion.length > LIMITS.assertion || assertion.includes(",")) {
    return deny(rt, 403, "exactly one Cloudflare Access assertion is required");
  }
  const headers = { [ASSERTION_HEADER]: assertion };
  for (const [name, max] of MCP_HEADERS[request.method]) {
    const value = request.headers.get(name);
    if (value === null) continue;
    if (value.length > max || !VISIBLE.test(value)) return deny(rt, 400, `${name} is not accepted`);
    headers[name] = value;
  }
  let body;
  if (request.method === "POST") {
    const declared = request.headers.get("Content-Length");
    if (declared !== null && !(/^\d{1,7}$/.test(declared) && Number(declared) <= LIMITS.body)) {
      return deny(rt, 413, "MCP message exceeds the size limit");
    }
    body = await withDeadline(rt, LIMITS.bodyMs, (signal) => readBounded(request.body, LIMITS.body, signal));
    if (body === TIMED_OUT) return deny(rt, 408, "MCP body read timed out");
    if (body === null) return deny(rt, 413, "MCP message exceeds the size limit");
  }
  const result = await withDeadline(rt, LIMITS.upstreamMs, async (signal) => {
    const response = await binding.fetch(UPSTREAM_ORIGIN + MCP_PATH, {
      method: request.method, headers, body, redirect: "manual", signal,
    });
    // A response that arrives after the deadline (the binding may ignore the
    // abort) is cancelled, never read.
    const out = signal.aborted || !MCP_STATUSES.has(response.status) ? null : replyHeaders(response.headers);
    if (out === null) {
      await cancelBody(response);
      return null;
    }
    const reply = await readBounded(response.body, LIMITS.reply, signal);
    if (reply === null || (reply.byteLength > 0 && (response.status === 204 || !out["Content-Type"]))) return null;
    return { status: response.status, headers: out, reply };
  });
  if (result === TIMED_OUT) return deny(rt, 504, "MCP upstream timed out");
  if (result === null) return deny(rt, 502, "MCP upstream reply refused");
  return new rt.Response(result.reply.byteLength === 0 ? null : result.reply, { status: result.status, headers: result.headers });
}

// replyHeaders returns the relayed response headers, or null when the media
// type or a relayed header is not the Go gateway's.
function replyHeaders(upstream) {
  const out = { ...SAFE };
  const type = upstream.get("Content-Type");
  if (type !== null) {
    if (!MCP_TYPES.includes(type)) return null;
    out["Content-Type"] = type;
  }
  for (const [name, pattern] of MCP_REPLY_HEADERS) {
    const value = upstream.get(name);
    if (value === null) continue;
    if (!pattern.test(value)) return null;
    out[name] = value;
  }
  return out;
}

async function forwardRegister(request, binding, rt, residentEnabled) {
  if (REGISTER_FORBIDDEN.some((name) => request.headers.has(name))) {
    return deny(rt, 403, "registration accepts no cookie, authorization or Access assertion");
  }
  const protocol = request.headers.get("Sec-WebSocket-Protocol");
  if (protocol !== SUBPROTOCOL && !(residentEnabled && protocol === RESIDENT_SUBPROTOCOL)) {
    return deny(rt, 400, "the fixed registration subprotocol is required");
  }
  const response = await withDeadline(rt, LIMITS.upgradeMs, async (signal) => {
    const answer = await binding.fetch(UPSTREAM_ORIGIN + REGISTER_PATH, {
      method: "GET", headers: { Upgrade: "websocket", "Sec-WebSocket-Protocol": protocol }, redirect: "manual", signal,
    });
    // An upgrade that arrives after the deadline is closed, never relayed.
    if (signal.aborted) {
      await discard(answer, 1011, "registration upstream timed out");
      return null;
    }
    return answer;
  });
  if (response === TIMED_OUT || response === null) return deny(rt, 504, "registration upstream timed out");
  const upstream = response.webSocket;
  if (response.status !== 101 || !upstream) {
    await discard(response, 1011, "registration upstream refused");
    const status = REGISTER_REFUSALS.includes(response.status) ? response.status : 502;
    const retry = response.headers?.get("Retry-After");
    return deny(rt, status, "registration upstream refused the WebSocket", status === 503 && /^\d{1,4}$/.test(retry ?? "") ? { "Retry-After": retry } : {});
  }
  if (response.headers.get("Sec-WebSocket-Protocol") !== protocol) {
    await discard(response, 1002, "registration subprotocol mismatch");
    return deny(rt, 502, "registration upstream subprotocol mismatch");
  }
  // The Worker sends nothing itself: the challenge, host proof and channel
  // handshake are the Go gateway's and the Mac host's.
  const [client, server] = rt.newPair();
  relay(server, upstream, rt, LIMITS);
  return new rt.Response(null, { status: 101, webSocket: client, headers: { "Sec-WebSocket-Protocol": protocol } });
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
