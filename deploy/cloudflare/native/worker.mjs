// Tether native route: Cloudflare Worker source candidate for the existing Go
// rescue-gateway.
//
// DENY ALL unless env.TETHER_NATIVE_GATE equals GATE_VALUE exactly. Even then
// only POST/DELETE MCP_PATH, OAuth protected-resource metadata GETs, a GET
// WebSocket upgrade at REGISTER_PATH, when TETHER_NATIVE_PAIRING opts in the
// pairing paths, and when TETHER_NATIVE_RESCUE opts in a GET WebSocket upgrade
// at RESCUE_PATH are forwarded, to fixed upstream paths, through the ONE VPC
// Service binding env.TETHER_NATIVE_VPC. There is no global fetch, no
// request-selected target and no redirect. This Worker authenticates
// nobody and issues no grant: for
// MCP the Go agentgateway verifies the configured Access assertion or OAuth
// bearer token and maps its subject;
// for registration the Go agenttransport verifies the enrolled host-key proof
// and the agent channel checks the same grant before any command. For pairing,
// the Go agentgateway verifies the existing route's bearer and key proof,
// operations bearer and key proof, or confirm consent binding and key proof.
// Nothing is logged.

// The WebSocket relay is the reviewed probe relay (explicit ArrayBuffer
// binaryType, one close, one lifetime timer); only the limits differ.
import { relay } from "../probe/worker.mjs";

export const MCP_PATH = "/mcp";
export const REGISTER_PATH = "/tether-agent/v1/register";
export const PAIR_PATH = "/tether-agent/v1/pair";
export const PAIR_OPERATIONS_PATH = `${PAIR_PATH}/operations`;
export const PAIR_CONFIRM_PATH = `${PAIR_PATH}/confirm`;
export const PAIR_CONSENT_PATH = `${PAIR_PATH}/consent`;
export const PAIR_CONSENT_CALLBACK_PATH = `${PAIR_CONSENT_PATH}/callback`;
export const RESCUE_PATH = "/tether-agent/v1/rescue";
export const SUBPROTOCOL = "rescue-agent-register.v1";
export const RESCUE_SUBPROTOCOL = "rescue-agent-reception.v1";
export const RESIDENT_SUBPROTOCOL = "rescue-agent-resident.v1";
export const RESIDENT_GATE_VALUE = "open-resident-tether-control";
export const ASSERTION_HEADER = "Cf-Access-Jwt-Assertion";
export const GATE_VALUE = "open-native-tether-gateway";
export const PAIRING_GATE_VALUE = "open-native-tether-pairing";
export const RESCUE_GATE_VALUE = "open-native-tether-rescue";
const OAUTH_BEARER_MODE = "oauth_bearer_jwt";
const OAUTH_METADATA_PATHS = ["/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"];
// URL for binding.fetch. The VPC Service sends to its own fixed host and port,
// so this supplies the path only; the Go gateway routes by exact path.
export const UPSTREAM_ORIGIN = "http://rescue-gateway.internal";

// body and reply match mcpadapter.MaxLine (512 KiB); the Go call timeout is
// 30 s, so upstreamMs bounds one MCP round trip including the reply body.
// message is agenttransport.MaxStreamMessage (registration text is 2 KiB and
// enforced by Go). lifetimeMs is the absolute pilot relay lifetime; nothing
// reconnects or extends it.
export const LIMITS = Object.freeze({
  assertion: 8192, authorization: 8199, challenge: 8192, body: 512 * 1024, pairBody: 4 * 1024, reply: 512 * 1024, bodyMs: 10_000, upstreamMs: 35_000, upgradeMs: 10_000,
  message: 32 * 1024, frames: 4096, lifetimeMs: 300_000,
});

// The rescue reception and its channel share one WSS, so its relay must
// outlast the longest session: the reception handshake (10 s), a code's
// lifetime (5 min), the gateway's request expiry (75 s) and the longest grant
// (24 h), plus slack. The frame budget keeps the rate of LIMITS. The host and
// the gateway still end the session at its expiry; this is the outer bound.
const RESCUE_LIFETIME_MS = 24 * 3_600_000 + 10 * 60_000;
const RESCUE_LIMITS = Object.freeze({
  ...LIMITS, lifetimeMs: RESCUE_LIFETIME_MS, frames: LIMITS.frames * (RESCUE_LIFETIME_MS / LIMITS.lifetimeMs),
});

// Request headers forwarded to MCP, per method, with their length bound.
// Credentials are added separately for the selected mode; other identity and
// forwarding headers are dropped. The Go gateway still validates each value.
const MCP_HEADERS = {
  POST: [["Content-Type", 128], ["Accept", 256], ["Mcp-Session-Id", 128], ["Mcp-Protocol-Version", 32]],
  DELETE: [["Mcp-Session-Id", 128], ["Mcp-Protocol-Version", 32]],
};
// Statuses the Go gateway answers for MCP requests. OAuth 401 challenges are
// handled separately and must pass their own validation.
const MCP_STATUSES = new Set([200, 202, 204, 400, 403, 404, 405, 406, 413, 415, 429, 500, 503]);
const MCP_TYPES = ["application/json", "text/plain; charset=utf-8"];
// Response headers relayed when well-formed. Location, Set-Cookie and every
// other header are dropped; OAuth 401 challenges use a separate allowlist.
const MCP_REPLY_HEADERS = [
  ["Mcp-Session-Id", /^[0-9a-f]{32}$/], ["Mcp-Protocol-Version", /^\d{4}-\d{2}-\d{2}$/],
  ["Retry-After", /^\d{1,4}$/], ["Allow", /^POST, DELETE$/],
];
// The pair route's answers and, per status, the headers each one must carry;
// every answer names its media type. The body bound is agentgateway.MaxPairBody.
const PAIR_STATUSES = new Set([200, 400, 401, 403, 404, 405, 409, 413, 503]);
const PAIR_REPLY_HEADERS = { 401: [["WWW-Authenticate", /^Bearer(?: error="invalid_token")?$/]], 405: [["Allow", /^POST$/]] };
const REGISTER_REFUSALS = [400, 403, 404, 405, 503];
const RESCUE_REFUSALS = [...REGISTER_REFUSALS, 429];
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
  const authMode = env.TETHER_NATIVE_AUTH_MODE;
  const metadataPath = authMode === OAUTH_BEARER_MODE ? OAUTH_METADATA_PATHS.find((path) => path === url.pathname) : undefined;
  const pair = env.TETHER_NATIVE_PAIRING === PAIRING_GATE_VALUE && url.pathname === PAIR_PATH;
  const pairOperations = env.TETHER_NATIVE_PAIRING === PAIRING_GATE_VALUE && url.pathname === PAIR_OPERATIONS_PATH;
  const pairConfirm = env.TETHER_NATIVE_PAIRING === PAIRING_GATE_VALUE && url.pathname === PAIR_CONFIRM_PATH;
  const pairConsent = env.TETHER_NATIVE_PAIRING === PAIRING_GATE_VALUE && url.pathname === PAIR_CONSENT_PATH;
  const pairConsentCallback = env.TETHER_NATIVE_PAIRING === PAIRING_GATE_VALUE && url.pathname === PAIR_CONSENT_CALLBACK_PATH;
  const rescue = env.TETHER_NATIVE_RESCUE === RESCUE_GATE_VALUE && url.pathname === RESCUE_PATH;
  const pairConsentFlow = pairOperations || pairConfirm || pairConsent || pairConsentCallback;
  if (url.pathname !== MCP_PATH && url.pathname !== REGISTER_PATH && !pair && !pairConsentFlow && !rescue && metadataPath === undefined) return deny(rt, 404, "not found");
  const hasQuery = url.search !== "" || request.url.includes("?");
  if (pairConsentFlow) {
    const queryAllowed = pairConsent && request.method === "GET"
      ? exactlyOneQueryValue(url, "operation")
      : pairConsentCallback && request.method === "GET"
        ? validConsentCallbackQuery(url)
        : !hasQuery;
    if (!queryAllowed) return deny(rt, 400, "pair consent query is not accepted");
  } else if (hasQuery) {
    return deny(rt, 400, "query parameters are not accepted");
  }
  // Reject observable raw-path aliases before normalized URL routing.
  const rawTarget = request.url.slice(url.origin.length);
  const queryAt = rawTarget.indexOf("?");
  const rawPath = queryAt < 0 ? rawTarget : rawTarget.slice(0, queryAt);
  if (rawPath !== url.pathname) return deny(rt, 404, "not found");
  const mcp = url.pathname === MCP_PATH;
  if (metadataPath !== undefined) {
    if (request.method !== "GET") return deny(rt, 405, "method not allowed", { Allow: "GET" });
  } else if (mcp) {
    // No GET stream is offered and no upgrade or CORS preflight is answered.
    if ((request.method !== "POST" && request.method !== "DELETE") || request.headers.has("Upgrade")) {
      return deny(rt, 405, "method not allowed", { Allow: "POST, DELETE" });
    }
  } else if (pair) {
    if (request.method !== "POST" || request.headers.has("Upgrade")) return deny(rt, 405, "method not allowed", { Allow: "POST" });
  } else if (pairOperations || pairConfirm || (pairConsent && request.method === "POST")) {
    if (request.method !== "POST" || request.headers.has("Upgrade")) return deny(rt, 405, "method not allowed", { Allow: "POST" });
  } else if (pairConsent) {
    if (request.method !== "GET" || request.headers.has("Upgrade")) return deny(rt, 405, "method not allowed", { Allow: "GET, POST" });
  } else if (pairConsentCallback) {
    if (request.method !== "GET" || request.headers.has("Upgrade")) return deny(rt, 405, "method not allowed", { Allow: "GET" });
  } else if (request.method !== "GET" || (request.headers.get("Upgrade") ?? "").toLowerCase() !== "websocket") {
    return deny(rt, 405, "method not allowed", { Allow: "GET" });
  }
  // Only the browser's consent form may carry Origin, and it must be same-origin.
  // Existing native routes continue to refuse Origin rather than forwarding it.
  if (pairConsent && request.method === "POST") {
    if (request.headers.get("Origin") !== url.origin) return deny(rt, 403, "origin not allowed");
  } else if (request.headers.has("Origin")) {
    return deny(rt, 403, "origin not allowed");
  }
  if (metadataPath !== undefined) return forwardProtectedResourceMetadata(metadataPath, binding, rt);
  if (mcp) return forwardMcp(request, binding, rt, authMode);
  if (pair) return forwardPair(request, binding, rt);
  if (pairOperations) return forwardPairStage(request, PAIR_OPERATIONS_PATH, binding, rt, true);
  if (pairConfirm) return forwardPairStage(request, PAIR_CONFIRM_PATH, binding, rt, false);
  if (pairConsent && request.method === "GET") return forwardPairConsent(request, PAIR_CONSENT_PATH, url.search, {}, undefined, binding, rt);
  if (pairConsent && request.method === "POST") return forwardPairConsentPost(request, binding, rt);
  if (pairConsentCallback) return forwardPairConsentCallback(request, consentCallbackQuery(url), binding, rt);
  if (rescue) return forwardRescue(request, binding, rt);
  return forwardRegister(request, binding, rt, env.TETHER_RESIDENT_GATE === RESIDENT_GATE_VALUE);
}

function exactlyOneQueryValue(url, name) {
  const keys = [...url.searchParams.keys()];
  return keys.length === 1 && keys[0] === name && url.searchParams.get(name) !== "";
}

function validConsentCallbackQuery(url) {
  const keys = [...url.searchParams.keys()];
  if (keys.length !== new Set(keys).size || url.searchParams.getAll("state").length !== 1 || url.searchParams.get("state") === "") return false;
  const hasCode = url.searchParams.has("code");
  const hasError = url.searchParams.has("error");
  const success = hasCode && !hasError && !url.searchParams.has("error_description") && url.searchParams.get("code") !== "";
  const denied = !hasCode && hasError && url.searchParams.get("error") !== "";
  return success || denied;
}

function consentCallbackQuery(url) {
  const query = new URLSearchParams();
  if (url.searchParams.has("code")) {
    query.set("code", url.searchParams.get("code"));
  } else {
    query.set("error", url.searchParams.get("error"));
    if (url.searchParams.has("error_description")) query.set("error_description", url.searchParams.get("error_description"));
  }
  query.set("state", url.searchParams.get("state"));
  return `?${query}`;
}

async function forwardPair(request, binding, rt) {
  const authorization = request.headers.get("Authorization");
  if (!authorization || authorization.length > LIMITS.authorization || authorization.includes(",")) {
    return deny(rt, 403, "exactly one bounded bearer credential is required");
  }
  const headers = { Authorization: authorization };
  const contentType = request.headers.get("Content-Type");
  if (contentType !== null) {
    if (contentType.length > 128 || !VISIBLE.test(contentType)) return deny(rt, 400, "Content-Type is not accepted");
    headers["Content-Type"] = contentType;
  }
  const declared = request.headers.get("Content-Length");
  if (declared !== null && !(/^\d{1,4}$/.test(declared) && Number(declared) <= LIMITS.pairBody)) {
    return deny(rt, 413, "pair body exceeds the size limit");
  }
  const body = await withDeadline(rt, LIMITS.bodyMs, (signal) => readBounded(request.body, LIMITS.pairBody, signal));
  if (body === TIMED_OUT) return deny(rt, 408, "pair body read timed out");
  if (body === null) return deny(rt, 413, "pair body exceeds the size limit");
  const result = await withDeadline(rt, LIMITS.upstreamMs, async (signal) => {
    const response = await binding.fetch(UPSTREAM_ORIGIN + PAIR_PATH, {
      method: "POST", headers, body, redirect: "manual", signal,
    });
    // A response that arrives after the deadline is cancelled, never read.
    const required = PAIR_REPLY_HEADERS[response.status] ?? [];
    const out = signal.aborted || !PAIR_STATUSES.has(response.status) ? null : replyHeaders(response.headers, required);
    if (out === null || !out["Content-Type"] || required.some(([name]) => !out[name])) {
      await cancelBody(response);
      return null;
    }
    const reply = await readBounded(response.body, LIMITS.reply, signal);
    if (reply === null) return null;
    return { status: response.status, headers: out, reply };
  });
  if (result === TIMED_OUT) return deny(rt, 504, "pair upstream timed out");
  if (result === null) return deny(rt, 502, "pair upstream reply refused");
  return new rt.Response(result.reply.byteLength === 0 ? null : result.reply, { status: result.status, headers: result.headers });
}

async function readPairBody(request, rt) {
  const declared = request.headers.get("Content-Length");
  if (declared !== null && !(/^\d{1,4}$/.test(declared) && Number(declared) <= LIMITS.pairBody)) {
    return { response: deny(rt, 413, "pair body exceeds the size limit") };
  }
  const body = await withDeadline(rt, LIMITS.bodyMs, (signal) => readBounded(request.body, LIMITS.pairBody, signal));
  if (body === TIMED_OUT) return { response: deny(rt, 408, "pair body read timed out") };
  if (body === null) return { response: deny(rt, 413, "pair body exceeds the size limit") };
  return { body };
}

async function forwardPairStage(request, path, binding, rt, requireBearer) {
  const headers = {};
  if (requireBearer) {
    const authorization = request.headers.get("Authorization");
    if (!authorization || authorization.length > LIMITS.authorization || authorization.includes(",")) {
      return deny(rt, 403, "exactly one bounded bearer credential is required");
    }
    headers.Authorization = authorization;
  }
  if (request.headers.get("Content-Type") !== "application/json") return deny(rt, 400, "Content-Type must be application/json");
  headers["Content-Type"] = "application/json";
  const input = await readPairBody(request, rt);
  if (input.response) return input.response;

  const result = await withDeadline(rt, LIMITS.upstreamMs, async (signal) => {
    const response = await binding.fetch(UPSTREAM_ORIGIN + path, {
      method: "POST", headers, body: input.body, redirect: "manual", signal,
    });
    if (signal.aborted) {
      await cancelBody(response);
      return null;
    }
    const reply = await readBounded(response.body, LIMITS.reply, signal);
    if (reply === null) return null;
    const responseHeaders = new Headers();
    const contentType = response.headers.get("Content-Type");
    if (contentType !== null) responseHeaders.set("Content-Type", contentType);
    return { status: response.status, headers: responseHeaders, reply };
  });
  if (result === TIMED_OUT) return deny(rt, 504, "pair upstream timed out");
  if (result === null) return deny(rt, 502, "pair upstream reply refused");
  return new rt.Response(result.reply.byteLength === 0 ? null : result.reply, { status: result.status, headers: result.headers });
}

async function forwardPairConsentPost(request, binding, rt) {
  if (request.headers.get("Content-Type") !== "application/x-www-form-urlencoded") {
    return deny(rt, 400, "Content-Type must be application/x-www-form-urlencoded");
  }
  const input = await readPairBody(request, rt);
  if (input.response) return input.response;
  const headers = { "Content-Type": "application/x-www-form-urlencoded" };
  const cookie = request.headers.get("Cookie");
  if (cookie !== null) headers.Cookie = cookie;
  return forwardPairConsent(request, PAIR_CONSENT_PATH, "", headers, input.body, binding, rt);
}

async function forwardPairConsentCallback(request, search, binding, rt) {
  const headers = {};
  const cookie = request.headers.get("Cookie");
  if (cookie !== null) headers.Cookie = cookie;
  return forwardPairConsent(request, PAIR_CONSENT_CALLBACK_PATH, search, headers, undefined, binding, rt);
}

async function forwardPairConsent(request, path, search, headers, body, binding, rt) {
  const result = await withDeadline(rt, LIMITS.upstreamMs, async (signal) => {
    const init = { method: request.method, headers, redirect: "manual", signal };
    if (body !== undefined) init.body = body;
    const response = await binding.fetch(UPSTREAM_ORIGIN + path + search, init);
    if (signal.aborted) {
      await cancelBody(response);
      return null;
    }
    const reply = await readBounded(response.body, LIMITS.reply, signal);
    if (reply === null) return null;
    return { status: response.status, headers: consentReplyHeaders(response.headers), reply };
  });
  if (result === TIMED_OUT) return deny(rt, 504, "pair consent upstream timed out");
  if (result === null) return deny(rt, 502, "pair consent upstream reply refused");
  return new rt.Response(result.reply.byteLength === 0 ? null : result.reply, { status: result.status, headers: result.headers });
}

function consentReplyHeaders(upstream) {
  const out = new Headers();
  for (const name of ["Cache-Control", "X-Frame-Options", "Content-Security-Policy", "Set-Cookie", "Location", "Content-Type"]) {
    if (name === "Set-Cookie" && typeof upstream.getSetCookie === "function") {
      for (const cookie of upstream.getSetCookie()) out.append(name, cookie);
      continue;
    }
    const value = upstream.get(name);
    if (value !== null) out.set(name, value);
  }
  return out;
}

async function forwardMcp(request, binding, rt, authMode) {
  let credential;
  if (authMode === OAUTH_BEARER_MODE) {
    credential = request.headers.get("Authorization");
    if (credential !== null && (!credential || credential.length > LIMITS.authorization || credential.includes(","))) {
      return deny(rt, 403, "exactly one bounded OAuth bearer credential is required");
    }
  } else {
    credential = request.headers.get(ASSERTION_HEADER);
    if (!credential || credential.length > LIMITS.assertion || credential.includes(",")) {
      return deny(rt, 403, "exactly one Cloudflare Access assertion is required");
    }
  }
  // Presence is an early refusal only, never authentication: the Go gateway
  // verifies the selected credential's signature, issuer, audience and lifetime.
  const headers = authMode === OAUTH_BEARER_MODE
    ? (credential === null ? {} : { Authorization: credential })
    : { [ASSERTION_HEADER]: credential };
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
    const oauthChallenge = authMode === OAUTH_BEARER_MODE && response.status === 401;
    const out = signal.aborted || (!MCP_STATUSES.has(response.status) && !oauthChallenge)
      ? null
      : (oauthChallenge ? oauthChallengeHeaders(response.headers) : replyHeaders(response.headers));
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

async function forwardProtectedResourceMetadata(path, binding, rt) {
  const result = await withDeadline(rt, LIMITS.upstreamMs, async (signal) => {
    const response = await binding.fetch(UPSTREAM_ORIGIN + path, {
      method: "GET", headers: {}, redirect: "manual", signal,
    });
    if (signal.aborted || response.status !== 200 || response.headers.get("Content-Type") !== "application/json") {
      await cancelBody(response);
      return null;
    }
    const reply = await readBounded(response.body, LIMITS.reply, signal);
    if (reply === null) return null;
    return { reply };
  });
  if (result === TIMED_OUT) return deny(rt, 504, "protected resource metadata upstream timed out");
  if (result === null) return deny(rt, 502, "protected resource metadata upstream reply refused");
  return new rt.Response(result.reply.byteLength === 0 ? null : result.reply, {
    status: 200, headers: { ...SAFE, "Content-Type": "application/json" },
  });
}

// replyHeaders returns the relayed response headers, or null when the media
// type or a relayed header is not the Go gateway's.
function replyHeaders(upstream, allowedHeaders = MCP_REPLY_HEADERS) {
  const out = { ...SAFE };
  const type = upstream.get("Content-Type");
  if (type !== null) {
    if (!MCP_TYPES.includes(type)) return null;
    out["Content-Type"] = type;
  }
  for (const [name, pattern] of allowedHeaders) {
    const value = upstream.get(name);
    if (value === null) continue;
    if (!pattern.test(value)) return null;
    out[name] = value;
  }
  return out;
}

function oauthChallengeHeaders(upstream) {
  const out = replyHeaders(upstream);
  const challenge = upstream.get("WWW-Authenticate");
  if (out === null || challenge === null || challenge.length > LIMITS.challenge) return null;
  const match = /^Bearer resource_metadata="([^"\x00-\x20\x7f]+)"(?:, [!#$%&'*+.^_`|~0-9A-Za-z-]+="[^"\x00-\x1f\x7f]*")*$/.exec(challenge);
  if (!match) return null;
  try {
    const metadataURL = new URL(match[1]);
    if (metadataURL.protocol !== "https:" || metadataURL.hostname === "" || metadataURL.username !== "" || metadataURL.password !== "" || metadataURL.hash !== "") return null;
  } catch {
    return null;
  }
  out["WWW-Authenticate"] = challenge;
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

async function forwardRescue(request, binding, rt) {
  if (REGISTER_FORBIDDEN.some((name) => request.headers.has(name))) {
    return deny(rt, 403, "rescue reception accepts no cookie, authorization or Access assertion");
  }
  const protocol = request.headers.get("Sec-WebSocket-Protocol");
  if (protocol !== RESCUE_SUBPROTOCOL) return deny(rt, 400, "the fixed rescue subprotocol is required");
  const response = await withDeadline(rt, LIMITS.upgradeMs, async (signal) => {
    const answer = await binding.fetch(UPSTREAM_ORIGIN + RESCUE_PATH, {
      method: "GET", headers: { Upgrade: "websocket", "Sec-WebSocket-Protocol": protocol }, redirect: "manual", signal,
    });
    if (signal.aborted) {
      await discard(answer, 1011, "rescue upstream timed out");
      return null;
    }
    return answer;
  });
  if (response === TIMED_OUT || response === null) return deny(rt, 504, "rescue upstream timed out");
  const upstream = response.webSocket;
  if (response.status !== 101 || !upstream) {
    await discard(response, 1011, "rescue upstream refused");
    const status = RESCUE_REFUSALS.includes(response.status) ? response.status : 502;
    const retry = response.headers?.get("Retry-After");
    return deny(rt, status, "rescue upstream refused the WebSocket", (status === 429 || status === 503) && /^\d{1,4}$/.test(retry ?? "") ? { "Retry-After": retry } : {});
  }
  if (response.headers.get("Sec-WebSocket-Protocol") !== protocol) {
    await discard(response, 1002, "rescue subprotocol mismatch");
    return deny(rt, 502, "rescue upstream subprotocol mismatch");
  }
  const [client, server] = rt.newPair();
  relay(server, upstream, rt, RESCUE_LIMITS);
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
