// node --test deploy/cloudflare/native/
// Fake binding, sockets and timers only: no network, Worker runtime,
// Cloudflare account or real assertion. Every value is synthetic.
import { test, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  createNativeWorker, MCP_PATH, REGISTER_PATH, SUBPROTOCOL, ASSERTION_HEADER, GATE_VALUE, UPSTREAM_ORIGIN, LIMITS,
} from "./worker.mjs";
import * as native from "./worker.mjs";
import * as probe from "../probe/worker.mjs";

const ORIGIN = "https://tether-native.example.invalid";
const TOKEN = "synthetic.assertion.value";
const SESSION = "0123456789abcdef0123456789abcdef";

class FakeSocket {
  constructor() { this.listeners = {}; this.sent = []; this.closed = null; this.accepted = false; this.binaryType = "blob"; }
  accept() { this.accepted = true; this.acceptedBinaryType = this.binaryType; }
  addEventListener(type, fn) { (this.listeners[type] ??= []).push(fn); }
  send(data) { if (this.closed) throw new Error("closed"); this.sent.push(data); }
  close(code, reason) { if (this.closed) throw new Error("closed twice"); this.closed = { code, reason }; }
  emit(type, event) { for (const fn of this.listeners[type] ?? []) fn(event); }
}

class FakeResponse {
  constructor(body, init = {}) {
    this.body = body; this.status = init.status ?? 200; this.headers = new Headers(init.headers); this.webSocket = init.webSocket ?? null;
  }
}

function fakeRuntime() {
  const timers = new Map();
  let next = 1;
  const pairs = [];
  return {
    pairs, timers,
    Response: FakeResponse,
    newPair: () => { const pair = [new FakeSocket(), new FakeSocket()]; pairs.push(pair); return pair; },
    setTimeout: (fn, ms) => { const id = next++; timers.set(id, { fn, ms }); return id; },
    clearTimeout: (id) => { timers.delete(id); },
    fireAll: () => { for (const [id, t] of [...timers]) { timers.delete(id); t.fn(); } },
  };
}

function fakeBinding(respond) {
  const calls = [];
  return { calls, fetch: (url, init) => { calls.push({ url, init }); return respond(url, init); } };
}

function request(path, { method = "POST", headers = {}, body = null } = {}) {
  const stream = body === null || body instanceof ReadableStream ? body : new Response(body).body;
  return { url: ORIGIN + path, method, headers: new Headers(headers), body: stream };
}

// trackedBody records pulls and cancellation; with hang, a read stays pending
// until the reader is cancelled.
function trackedBody(chunks = [], { hang = false, fail = false } = {}) {
  const state = { pulls: 0, cancelled: false };
  const queue = [...chunks];
  const stream = new ReadableStream({
    pull(controller) {
      state.pulls++;
      if (queue.length > 0) controller.enqueue(new TextEncoder().encode(queue.shift()));
      else if (fail) controller.error(new Error("synthetic stream failure"));
      else if (!hang) controller.close();
    },
    cancel() { state.cancelled = true; },
  }, { highWaterMark: 0 });
  return { stream, state };
}

function deferred() {
  let resolve;
  const promise = new Promise((r) => { resolve = r; });
  return { promise, resolve };
}

async function until(what, condition) {
  for (let i = 0; i < 200 && !condition(); i++) await new Promise((resolve) => setImmediate(resolve));
  assert.ok(condition(), what);
}

const INIT = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}';
const mcpHeaders = { [ASSERTION_HEADER]: TOKEN, "Content-Type": "application/json", Accept: "application/json, text/event-stream" };
const regHeaders = { Upgrade: "websocket", "Sec-WebSocket-Protocol": SUBPROTOCOL };
const openEnv = (binding) => ({ TETHER_NATIVE_GATE: GATE_VALUE, TETHER_NATIVE_VPC: binding });
const json = (body = '{"jsonrpc":"2.0","id":1,"result":{}}', status = 200, extra = {}) =>
  new Response(body, { status, headers: { "Content-Type": "application/json", ...extra } });
const text = (r) => new TextDecoder().decode(r.body);

function upgraded(protocol = SUBPROTOCOL) {
  const upstream = new FakeSocket();
  return { upstream, respond: () => ({ status: 101, headers: new Headers({ "Sec-WebSocket-Protocol": protocol }), webSocket: upstream, body: null }) };
}

let realFetch;
beforeEach(() => { realFetch = globalThis.fetch; globalThis.fetch = () => { throw new Error("global fetch must never be used"); }; });
afterEach(() => { globalThis.fetch = realFetch; });

test("deny all by default: no gate, wrong gate, the probe gate or a missing binding forwards nothing", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(() => json());
  const worker = createNativeWorker(rt);
  assert.notEqual(GATE_VALUE, probe.GATE_VALUE);
  for (const env of [undefined, {}, { TETHER_NATIVE_VPC: binding }, { TETHER_NATIVE_GATE: "true", TETHER_NATIVE_VPC: binding },
    { TETHER_NATIVE_GATE: "closed", TETHER_NATIVE_VPC: binding }, { TETHER_NATIVE_GATE: probe.GATE_VALUE, TETHER_NATIVE_VPC: binding },
    { TETHER_PROBE_GATE: probe.GATE_VALUE, TETHER_PROBE_VPC: binding }, { TETHER_PROBE_GATE: GATE_VALUE, TETHER_NATIVE_VPC: binding }]) {
    assert.equal((await worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: INIT }), env)).status, 404);
    assert.equal((await worker.fetch(request(REGISTER_PATH, { method: "GET", headers: regHeaders }), env)).status, 404);
  }
  // The open gate needs the native binding name; the probe's is not read.
  assert.equal((await worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: INIT }), { TETHER_NATIVE_GATE: GATE_VALUE, TETHER_PROBE_VPC: binding })).status, 503);
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
  assert.equal(rt.pairs.length, 0);
});

test("a closed gate reads no body and starts no timer or socket", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(upgraded().respond);
  const closed = { TETHER_NATIVE_GATE: "closed", TETHER_NATIVE_VPC: binding };
  const bodies = [];
  for (const [path, method, headers] of [[MCP_PATH, "POST", mcpHeaders], [MCP_PATH, "DELETE", mcpHeaders], [REGISTER_PATH, "GET", regHeaders], ["/admin", "POST", mcpHeaders]]) {
    const tracked = trackedBody([INIT]);
    bodies.push(tracked.state);
    assert.equal((await createNativeWorker(rt).fetch(request(path, { method, headers, body: method === "GET" ? null : tracked.stream }), closed)).status, 404);
  }
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
  assert.ok(bodies.every((s) => s.pulls === 0 && !s.cancelled));
});

test("only the exact native paths and methods forward; probe paths, metadata and query are refused", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(() => json());
  const worker = createNativeWorker(rt);
  const cases = [
    [probe.HTTP_PATH, "POST", mcpHeaders, 404], [probe.WS_PATH, "GET", regHeaders, 404], ["/", "POST", mcpHeaders, 404],
    ["/mcp/", "POST", mcpHeaders, 404], ["/MCP", "POST", mcpHeaders, 404], ["/%6Dcp", "POST", mcpHeaders, 404], ["/mcp/x", "POST", mcpHeaders, 404],
    ["/x/../mcp", "POST", mcpHeaders, 404], ["/%2e/mcp", "POST", mcpHeaders, 404], ["/x/%2e%2e/mcp", "POST", mcpHeaders, 404],
    ["/x/../tether-agent/v1/register", "GET", regHeaders, 404],
    [REGISTER_PATH + "/", "GET", regHeaders, 404], ["/.well-known/oauth-protected-resource", "GET", {}, 404],
    ["/.well-known/oauth-protected-resource/mcp", "GET", {}, 404], ["/.well-known/oauth-authorization-server", "GET", {}, 404],
    [MCP_PATH + "?target=elsewhere", "POST", mcpHeaders, 400], [MCP_PATH + "?", "POST", mcpHeaders, 400],
    [MCP_PATH + "?access_token=synthetic", "POST", mcpHeaders, 400], [REGISTER_PATH + "?token=synthetic", "GET", regHeaders, 400],
    [MCP_PATH, "GET", { ...mcpHeaders, Accept: "text/event-stream" }, 405], [MCP_PATH, "PUT", mcpHeaders, 405], [MCP_PATH, "PATCH", mcpHeaders, 405],
    [MCP_PATH, "OPTIONS", { Origin: ORIGIN, "Access-Control-Request-Method": "POST" }, 405], [MCP_PATH, "HEAD", mcpHeaders, 405],
    [MCP_PATH, "POST", { ...mcpHeaders, Upgrade: "websocket" }, 405], [MCP_PATH, "GET", { ...mcpHeaders, ...regHeaders }, 405],
    [REGISTER_PATH, "POST", regHeaders, 405], [REGISTER_PATH, "GET", { "Sec-WebSocket-Protocol": SUBPROTOCOL }, 405],
    [REGISTER_PATH, "GET", { ...regHeaders, Upgrade: "h2c" }, 405],
  ];
  for (const [path, method, headers, status] of cases) {
    const r = await worker.fetch(request(path, { method, headers, body: method === "POST" ? INIT : null }), openEnv(binding));
    assert.equal(r.status, status, `${method} ${path}`);
    for (const name of r.headers.keys()) assert.ok(!name.startsWith("access-control-"), `${method} ${path} ${name}`);
  }
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
});

test("any Origin is refused before forwarding, for MCP and registration; no CORS is offered", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(() => json());
  const worker = createNativeWorker(rt);
  for (const origin of [ORIGIN, "null", ""]) {
    for (const r of [
      await worker.fetch(request(MCP_PATH, { headers: { ...mcpHeaders, Origin: origin }, body: INIT }), openEnv(binding)),
      await worker.fetch(request(MCP_PATH, { method: "DELETE", headers: { ...mcpHeaders, Origin: origin } }), openEnv(binding)),
      await worker.fetch(request(REGISTER_PATH, { method: "GET", headers: { ...regHeaders, Origin: origin } }), openEnv(binding)),
    ]) {
      assert.equal(r.status, 403);
      assert.equal(r.headers.get("Access-Control-Allow-Origin"), null);
    }
  }
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
  assert.equal(rt.pairs.length, 0);
});

test("MCP needs one bounded assertion; bearer, cookie or identity headers alone are refused early", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const none = fakeBinding(() => json());
  const { [ASSERTION_HEADER]: _, ...unasserted } = mcpHeaders;
  for (const headers of [unasserted, { ...unasserted, Authorization: "Bearer synthetic-access-token" }, { ...unasserted, Cookie: "CF_Authorization=synthetic" },
    { ...unasserted, "Cf-Access-Authenticated-User-Email": "member@example.invalid", "X-Principal": "member-a" },
    { ...mcpHeaders, [ASSERTION_HEADER]: `${TOKEN}, ${TOKEN}` }, { ...mcpHeaders, [ASSERTION_HEADER]: "a".repeat(LIMITS.assertion + 1) }]) {
    assert.equal((await worker.fetch(request(MCP_PATH, { headers, body: INIT }), openEnv(none))).status, 403);
    assert.equal((await worker.fetch(request(MCP_PATH, { method: "DELETE", headers }), openEnv(none))).status, 403);
  }
  assert.equal(none.calls.length, 0);
  // A present but unverifiable assertion is forwarded and the Go gateway's 403 stands.
  const backend = fakeBinding(() => new Response("the Cloudflare Access assertion is not valid\n", { status: 403, headers: { "Content-Type": "text/plain; charset=utf-8" } }));
  const r = await worker.fetch(request(MCP_PATH, { headers: { ...mcpHeaders, [ASSERTION_HEADER]: "synthetic-not-a-jwt" }, body: INIT }), openEnv(backend));
  assert.equal(r.status, 403);
  assert.match(text(r), /not valid/);
  assert.equal(backend.calls.length, 1);
});

test("OAuth mode forwards Authorization without requiring an Access assertion", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const challenge = `Bearer resource_metadata="${ORIGIN}/.well-known/oauth-protected-resource/mcp", error="invalid_token"`;
  const binding = fakeBinding((_url, init) => init.headers.Authorization === undefined
    ? new Response("Unauthorized\n", { status: 401, headers: { "Content-Type": "text/plain; charset=utf-8", "WWW-Authenticate": challenge } })
    : json());
  const env = { ...openEnv(binding), TETHER_NATIVE_AUTH_MODE: "oauth_bearer_jwt" };
  const authorization = "Bearer synthetic-access-token";
  const postHeaders = { Authorization: authorization, "Content-Type": "application/json", Accept: "application/json, text/event-stream" };

  assert.equal((await worker.fetch(request(MCP_PATH, { headers: postHeaders, body: INIT }), env)).status, 200);
  assert.deepEqual(binding.calls[0].init.headers, {
    Authorization: authorization, "Content-Type": "application/json", Accept: "application/json, text/event-stream",
  });

  assert.equal((await worker.fetch(request(MCP_PATH, {
    method: "DELETE", headers: { Authorization: authorization, "Mcp-Session-Id": SESSION },
  }), env)).status, 200);
  assert.deepEqual(binding.calls[1].init.headers, { Authorization: authorization, "Mcp-Session-Id": SESSION });

  // A tokenless MCP initialize must reach Go so Claude can start OAuth discovery.
  const noAuth = { "Content-Type": "application/json", Accept: "application/json, text/event-stream" };
  const unauthenticated = await worker.fetch(request(MCP_PATH, { headers: noAuth, body: INIT }), env);
  assert.equal(unauthenticated.status, 401);
  assert.equal(unauthenticated.headers.get("WWW-Authenticate"), challenge);
  assert.equal(binding.calls[2].init.headers.Authorization, undefined);
  const duplicate = new Headers(postHeaders);
  duplicate.append("Authorization", "Bearer second-token");
  for (const headers of [duplicate, { ...postHeaders, Authorization: "Bearer one, Bearer two" },
    { ...postHeaders, Authorization: "a".repeat(LIMITS.authorization + 1) }]) {
    assert.equal((await worker.fetch(request(MCP_PATH, { headers, body: INIT }), env)).status, 403);
  }
  assert.equal((await worker.fetch(request(MCP_PATH, { headers: postHeaders, body: INIT }), {
    ...openEnv(binding), TETHER_NATIVE_AUTH_MODE: "unknown",
  })).status, 403);
  assert.equal(binding.calls.length, 3);
});

test("OAuth mode serves both protected-resource metadata paths as bounded JSON GETs", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const payload = JSON.stringify({ resource: `${ORIGIN}/mcp`, authorization_servers: ["https://issuer.example.invalid"] });
  const binding = fakeBinding(() => new Response(payload, { headers: {
    "Content-Type": "application/json", "Set-Cookie": "s=synthetic", Location: "https://elsewhere.invalid/",
  } }));
  const env = { ...openEnv(binding), TETHER_NATIVE_AUTH_MODE: "oauth_bearer_jwt" };
  const paths = ["/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"];

  // MCP clients fetch either RFC 9728 location after the tokenless /mcp challenge.
  for (const path of paths) {
    const response = await worker.fetch(request(path, { method: "GET" }), env);
    assert.equal(response.status, 200);
    assert.equal(response.headers.get("Content-Type"), "application/json");
    assert.equal(response.headers.get("Set-Cookie"), null);
    assert.equal(response.headers.get("Location"), null);
    assert.equal(text(response), payload);
  }
  assert.deepEqual(binding.calls.map(({ url, init }) => [url, init.method, init.body, init.redirect, init.headers]), paths.map((path) => [UPSTREAM_ORIGIN + path, "GET", undefined, "manual", {}]));

  for (const [path, options, status] of [
    [paths[0] + "?x=1", { method: "GET" }, 400],
    [paths[0], { method: "GET", headers: { Origin: ORIGIN } }, 403],
    [paths[0], { method: "POST", body: "{}" }, 405],
    [paths[0], { method: "HEAD" }, 405],
  ]) {
    assert.equal((await worker.fetch(request(path, options), env)).status, status);
  }
  assert.equal(binding.calls.length, 2);

  for (const respond of [
    () => new Response(payload, { status: 201, headers: { "Content-Type": "application/json" } }),
    () => new Response(payload, { headers: { "Content-Type": "application/json; charset=utf-8" } }),
    () => new Response("x".repeat(LIMITS.reply + 1), { headers: { "Content-Type": "application/json" } }),
  ]) {
    const invalid = fakeBinding(respond);
    assert.equal((await worker.fetch(request(paths[0], { method: "GET" }), { ...env, TETHER_NATIVE_VPC: invalid })).status, 502);
    assert.equal(invalid.calls.length, 1);
  }
});

test("OAuth mode refuses malformed, non-HTTPS, duplicate or oversized challenges", async () => {
  const worker = createNativeWorker(fakeRuntime());
  const env = { TETHER_NATIVE_GATE: GATE_VALUE, TETHER_NATIVE_AUTH_MODE: "oauth_bearer_jwt" };
  const send = async (challenge) => {
    const binding = fakeBinding(() => ({
      status: 401,
      headers: { get: (name) => name.toLowerCase() === "content-type" ? "text/plain; charset=utf-8" : name.toLowerCase() === "www-authenticate" ? challenge : null },
      body: new Response("Unauthorized\n").body,
    }));
    return worker.fetch(request(MCP_PATH, { headers: { "Content-Type": "application/json", Accept: "application/json, text/event-stream" }, body: INIT }), {
      ...env, TETHER_NATIVE_VPC: binding,
    });
  };

  // The relay accepts only one bounded HTTPS resource-metadata challenge from Go.
  for (const challenge of [
    'Bearer resource_metadata="http://elsewhere.invalid/.well-known/oauth-protected-resource/mcp"',
    'Bearer resource_metadata="https://elsewhere.invalid/.well-known/oauth-protected-resource/mcp"\r\nBasic realm="other"',
    'Bearer resource_metadata="https://elsewhere.invalid/.well-known/oauth-protected-resource/mcp", Basic realm="other"',
    `Bearer resource_metadata="https://elsewhere.invalid/${"x".repeat(LIMITS.authorization)}"`,
  ]) assert.equal((await send(challenge)).status, 502);
});

test("default mode keeps both protected-resource metadata paths hidden", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(() => json());
  const worker = createNativeWorker(rt);
  for (const path of ["/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"]) {
    assert.equal((await worker.fetch(request(path, { method: "GET" }), openEnv(binding))).status, 404);
  }
  assert.equal(binding.calls.length, 0);
});

test("MCP POST forwards only the fixed URL, assertion, MCP headers and bounded body; no credential or identity spoof", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(() => json(undefined, 200, { "Mcp-Session-Id": SESSION, "Set-Cookie": "s=synthetic", "WWW-Authenticate": "Bearer x", Location: "https://elsewhere.invalid/" }));
  const r = await createNativeWorker(rt).fetch(request(MCP_PATH, {
    headers: {
      ...mcpHeaders, "Mcp-Session-Id": SESSION, "Mcp-Protocol-Version": "2025-06-18", Cookie: "CF_Authorization=synthetic",
      Authorization: "Bearer synthetic", "Proxy-Authorization": "Basic synthetic", "Cf-Access-Authenticated-User-Email": "other@example.invalid",
      "X-Forwarded-For": "192.0.2.1", "X-Principal": "member-b", "X-Target": "http://elsewhere.invalid", Host: "elsewhere.invalid",
    },
    body: INIT,
  }), openEnv(binding));
  assert.equal(r.status, 200);
  assert.deepEqual(Object.fromEntries(r.headers), {
    "cache-control": "no-store", "content-type": "application/json", "mcp-session-id": SESSION, "x-content-type-options": "nosniff",
  });
  assert.match(text(r), /"result"/);
  const [{ url, init }] = binding.calls;
  assert.equal(url, UPSTREAM_ORIGIN + MCP_PATH);
  assert.deepEqual(init.headers, {
    [ASSERTION_HEADER]: TOKEN, "Content-Type": "application/json", Accept: "application/json, text/event-stream",
    "Mcp-Session-Id": SESSION, "Mcp-Protocol-Version": "2025-06-18",
  });
  assert.equal(init.method, "POST");
  assert.equal(init.redirect, "manual");
  assert.equal(new TextDecoder().decode(init.body), INIT);
  assert.equal(rt.timers.size, 0);
});

test("MCP DELETE forwards no body or media headers and relays 204 without a body", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(() => ({ status: 204, headers: new Headers(), body: null }));
  const tracked = trackedBody(["synthetic"]);
  const r = await createNativeWorker(rt).fetch(request(MCP_PATH, {
    method: "DELETE", headers: { ...mcpHeaders, "Mcp-Session-Id": SESSION, "Mcp-Protocol-Version": "2025-06-18", Cookie: "c=synthetic" }, body: tracked.stream,
  }), openEnv(binding));
  assert.equal(r.status, 204);
  assert.equal(r.body, null);
  const [{ init }] = binding.calls;
  assert.equal(init.method, "DELETE");
  assert.equal(init.body, undefined);
  assert.deepEqual(init.headers, { [ASSERTION_HEADER]: TOKEN, "Mcp-Session-Id": SESSION, "Mcp-Protocol-Version": "2025-06-18" });
  assert.equal(tracked.state.pulls, 0);
  assert.equal(rt.timers.size, 0);
});

test("oversized MCP header values are refused before forwarding", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => json());
  for (const extra of [{ Accept: "a".repeat(257) }, { "Content-Type": "application/json;" + "x".repeat(128) },
    { "Mcp-Session-Id": "a".repeat(129) }, { "Mcp-Protocol-Version": "2".repeat(33) }]) {
    assert.equal((await worker.fetch(request(MCP_PATH, { headers: { ...mcpHeaders, ...extra }, body: INIT }), openEnv(binding))).status, 400);
  }
  assert.equal(binding.calls.length, 0);
});

test("MCP bodies are bounded at 512 KiB and a stalled body times out with its reader cancelled", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => json());
  assert.equal(LIMITS.body, 512 * 1024);
  assert.equal((await worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: "x".repeat(LIMITS.body + 1) }), openEnv(binding))).status, 413);
  for (const declared of ["999999999", "-1", "1e3", String(LIMITS.body + 1)]) {
    assert.equal((await worker.fetch(request(MCP_PATH, { headers: { ...mcpHeaders, "Content-Length": declared }, body: "x" }), openEnv(binding))).status, 413);
  }
  assert.equal(binding.calls.length, 0);
  assert.equal((await worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: "x".repeat(LIMITS.body) }), openEnv(binding))).status, 200);
  assert.equal(binding.calls[0].init.body.byteLength, LIMITS.body);

  const { stream, state } = trackedBody(["partial"], { hang: true });
  const pending = worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: stream }), openEnv(binding));
  await until("body read pending", () => state.pulls === 2);
  assert.deepEqual([...rt.timers.values()].map((t) => t.ms), [LIMITS.bodyMs]);
  rt.fireAll();
  assert.equal((await pending).status, 408);
  await until("reader cancelled", () => state.cancelled);
  assert.equal(binding.calls.length, 1);
  assert.equal(rt.timers.size, 0);
});

test("MCP replies: redirects, 401, foreign media, malformed headers and oversize are refused; safe headers survive", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const send = async (respond) => worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: INIT }), openEnv(fakeBinding(respond)));
  for (const status of [301, 302, 303, 307, 308]) {
    const r = await send(() => new Response(null, { status, headers: { Location: "https://elsewhere.invalid/", "Set-Cookie": "s=synthetic" } }));
    assert.equal(r.status, 502, String(status));
    assert.equal(r.headers.get("Location"), null);
    assert.equal(r.headers.get("Set-Cookie"), null);
  }
  assert.equal((await send(() => new Response("x", { status: 401, headers: { "WWW-Authenticate": 'Bearer resource_metadata="https://elsewhere.invalid"' } }))).status, 502);
  assert.equal((await send(() => new Response("<html>", { status: 200, headers: { "Content-Type": "text/html" } }))).status, 502);
  assert.equal((await send(() => new Response("x", { status: 200 }))).status, 502); // Node supplies text/plain;charset=UTF-8
  assert.equal((await send(() => json(undefined, 200, { "Mcp-Session-Id": "not-a-session" }))).status, 502);
  assert.equal((await send(() => ({ status: 204, headers: new Headers(), body: new Response("x").body }))).status, 502);
  assert.equal((await send(() => ({ status: 200, headers: new Headers(), body: new Response("x").body }))).status, 502);
  assert.equal((await send(() => json("y".repeat(LIMITS.reply + 1)))).status, 502);
  assert.equal((await send(() => json("y".repeat(LIMITS.reply)))).status, 200);

  const busy = await send(() => new Response("too many concurrent requests\n", { status: 503, headers: { "Content-Type": "text/plain; charset=utf-8", "Retry-After": "1" } }));
  assert.equal(busy.status, 503);
  assert.equal(busy.headers.get("Retry-After"), "1");
  const stream = await send(() => new Response("no streams\n", { status: 405, headers: { "Content-Type": "text/plain; charset=utf-8", Allow: "POST, DELETE" } }));
  assert.equal(stream.headers.get("Allow"), "POST, DELETE");
  const accepted = await send(() => ({ status: 202, headers: new Headers(), body: null }));
  assert.equal(accepted.status, 202);
  assert.equal(accepted.body, null);
  assert.equal(rt.timers.size, 0);
});

test("the MCP upstream deadline is 35 s: a hung call aborts, a late response and a stalled reply are cancelled", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  let aborted = false;
  const hang = fakeBinding((_, init) => new Promise((_, reject) => init.signal.addEventListener("abort", () => { aborted = true; reject(new Error("aborted")); })));
  let pending = worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: INIT }), openEnv(hang));
  await until("upstream call", () => hang.calls.length === 1);
  assert.deepEqual([...rt.timers.values()].map((t) => t.ms), [35_000]);
  rt.fireAll();
  assert.equal((await pending).status, 504);
  assert.ok(aborted);

  const late = deferred();
  const ignoring = fakeBinding(() => late.promise);
  pending = worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: INIT }), openEnv(ignoring));
  await until("upstream call", () => ignoring.calls.length === 1);
  rt.fireAll();
  assert.equal((await pending).status, 504);
  const lateBody = trackedBody(['{"late":true}']);
  late.resolve({ status: 200, headers: new Headers({ "Content-Type": "application/json" }), body: lateBody.stream });
  await until("late body cancelled", () => lateBody.state.cancelled);
  assert.equal(lateBody.state.pulls, 0);

  const stalled = trackedBody([], { hang: true });
  const slow = fakeBinding(() => ({ status: 200, headers: new Headers({ "Content-Type": "application/json" }), body: stalled.stream }));
  pending = worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: INIT }), openEnv(slow));
  await until("reply read pending", () => stalled.state.pulls === 1);
  rt.fireAll();
  assert.equal((await pending).status, 504);
  await until("reply reader cancelled", () => stalled.state.cancelled);
  assert.equal(rt.timers.size, 0);
});

test("registration forwards only the fixed upgrade and subprotocol; the Worker adds no credential and sends nothing", async () => {
  const rt = fakeRuntime();
  const { upstream, respond } = upgraded();
  const binding = fakeBinding(respond);
  const r = await createNativeWorker(rt).fetch(request(REGISTER_PATH, {
    method: "GET", headers: { ...regHeaders, "X-Forwarded-For": "192.0.2.1", "X-Host-Label": "host-a", "Sec-WebSocket-Extensions": "permessage-deflate" },
  }), openEnv(binding));
  assert.equal(r.status, 101);
  assert.equal(r.headers.get("Sec-WebSocket-Protocol"), SUBPROTOCOL);
  const [client, server] = rt.pairs[0];
  assert.equal(r.webSocket, client);
  const [{ url, init }] = binding.calls;
  assert.equal(url, UPSTREAM_ORIGIN + REGISTER_PATH);
  assert.deepEqual(init.headers, { Upgrade: "websocket", "Sec-WebSocket-Protocol": SUBPROTOCOL });
  assert.equal(init.method, "GET");
  assert.equal(init.redirect, "manual");
  // The challenge, host proof and channel handshake are Go's and the Mac's.
  assert.deepEqual(server.sent, []);
  assert.deepEqual(upstream.sent, []);
});

test("registration refuses cookies, authorization, Access assertions and any other subprotocol before forwarding", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(upgraded().respond);
  for (const extra of [{ Cookie: "CF_Authorization=synthetic" }, { Authorization: "Bearer synthetic" }, { "Proxy-Authorization": "Basic synthetic" },
    { [ASSERTION_HEADER]: TOKEN }]) {
    assert.equal((await worker.fetch(request(REGISTER_PATH, { method: "GET", headers: { ...regHeaders, ...extra } }), openEnv(binding))).status, 403);
  }
  for (const protocol of [undefined, probe.SUBPROTOCOL, "rescue-agent-register.v2", `${SUBPROTOCOL}, other`, `${SUBPROTOCOL}, ${SUBPROTOCOL}`]) {
    const headers = { ...regHeaders };
    if (protocol === undefined) delete headers["Sec-WebSocket-Protocol"]; else headers["Sec-WebSocket-Protocol"] = protocol;
    assert.equal((await worker.fetch(request(REGISTER_PATH, { method: "GET", headers }), openEnv(binding))).status, 400);
  }
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.pairs.length, 0);
  assert.equal(rt.timers.size, 0);
});

test("registration relays only a 101 with the exact subprotocol; refusals stay refusals", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const send = (respond) => worker.fetch(request(REGISTER_PATH, { method: "GET", headers: regHeaders }), openEnv(fakeBinding(respond)));
  assert.equal((await send(() => new Response("Forbidden\n", { status: 403 }))).status, 403);
  const busy = await send(() => new Response("Service Unavailable\n", { status: 503, headers: { "Retry-After": "1" } }));
  assert.equal(busy.status, 503);
  assert.equal(busy.headers.get("Retry-After"), "1");
  assert.equal((await send(() => new Response("ok", { status: 200 }))).status, 502);
  assert.equal((await send(() => new Response(null, { status: 302, headers: { Location: "https://elsewhere.invalid/" } }))).headers.get("Location"), null);
  assert.equal((await send(() => new Response("teapot", { status: 418 }))).status, 502);
  const mismatch = upgraded(probe.SUBPROTOCOL);
  assert.equal((await send(mismatch.respond)).status, 502);
  assert.equal(mismatch.upstream.closed.code, 1002);
  const odd = new FakeSocket();
  assert.equal((await send(() => ({ status: 200, headers: new Headers(), webSocket: odd, body: null }))).status, 502);
  assert.ok(odd.accepted && odd.closed);
  assert.equal(rt.pairs.length, 0);
  assert.equal(rt.timers.size, 0);
});

test("the registration upgrade deadline is 10 s and a late upgrade is closed, never relayed", async () => {
  const rt = fakeRuntime();
  const late = deferred();
  const binding = fakeBinding(() => late.promise); // ignores the abort
  const pending = createNativeWorker(rt).fetch(request(REGISTER_PATH, { method: "GET", headers: regHeaders }), openEnv(binding));
  await until("upstream call", () => binding.calls.length === 1);
  assert.deepEqual([...rt.timers.values()].map((t) => t.ms), [10_000]);
  rt.fireAll();
  assert.equal((await pending).status, 504);
  const upstream = new FakeSocket();
  late.resolve({ status: 101, headers: new Headers({ "Sec-WebSocket-Protocol": SUBPROTOCOL }), webSocket: upstream, body: null });
  await until("late socket closed", () => upstream.closed !== null);
  assert.ok(upstream.accepted);
  assert.equal(upstream.closed.code, 1011);
  assert.equal(upstream.listeners.message, undefined);
  assert.equal(rt.pairs.length, 0);
  assert.equal(rt.timers.size, 0);
  assert.equal(binding.calls.length, 1);
});

async function relayedRegistration() {
  const rt = fakeRuntime();
  const { upstream, respond } = upgraded();
  const binding = fakeBinding(respond);
  const r = await createNativeWorker(rt).fetch(request(REGISTER_PATH, { method: "GET", headers: regHeaders }), openEnv(binding));
  assert.equal(r.status, 101);
  const [, server] = rt.pairs[0];
  return { rt, binding, server, upstream };
}

test("registration reuses the probe relay: ArrayBuffer before accept, text and binary kept", async () => {
  const { server, upstream } = await relayedRegistration();
  for (const ws of [server, upstream]) {
    assert.ok(ws.accepted);
    assert.equal(ws.acceptedBinaryType, "arraybuffer");
  }
  const challenge = '{"type":"challenge","nonce":"' + "A".repeat(43) + '"}';
  const register = "r".repeat(2048);
  const frame = new Uint8Array(LIMITS.message).buffer;
  upstream.emit("message", { data: challenge });
  server.emit("message", { data: register });
  upstream.emit("message", { data: '{"type":"accepted"}' });
  server.emit("message", { data: frame });
  upstream.emit("message", { data: frame });
  assert.deepEqual(server.sent, [challenge, '{"type":"accepted"}', frame]);
  assert.deepEqual(upstream.sent, [register, frame]);
  assert.ok(upstream.sent[1] instanceof ArrayBuffer);
  assert.equal(server.closed, null);
});

test("the relay uses the native 32 KiB message limit and a finite frame budget", async () => {
  let r = await relayedRegistration();
  r.server.emit("message", { data: new Uint8Array(LIMITS.message + 1).buffer });
  assert.equal(r.upstream.sent.length, 0);
  assert.equal(r.upstream.closed.code, 1009);
  assert.equal(r.rt.timers.size, 0);

  r = await relayedRegistration();
  r.server.emit("message", { data: new Blob(["x"]) });
  assert.equal(r.upstream.closed.code, 1009);

  r = await relayedRegistration();
  assert.ok(Number.isInteger(LIMITS.frames) && LIMITS.frames > 0 && LIMITS.frames <= 4096);
  for (let i = 0; i <= LIMITS.frames; i++) r.upstream.emit("message", { data: "f" });
  assert.equal(r.server.sent.length, LIMITS.frames);
  assert.equal(r.server.closed.code, 1008);
  assert.equal(r.upstream.closed.code, 1008);
  assert.equal(r.rt.timers.size, 0);
});

test("the relay lifetime is one absolute 5 min timer; ending it closes both ends and nothing reconnects", async () => {
  const { rt, binding, server, upstream } = await relayedRegistration();
  assert.deepEqual([...rt.timers.values()].map((t) => t.ms), [5 * 60 * 1000]);
  server.emit("message", { data: "busy" });
  upstream.emit("message", { data: "busy" });
  assert.equal(rt.timers.size, 1);
  rt.fireAll();
  assert.equal(server.closed.code, 4000);
  assert.equal(upstream.closed.code, 4000);
  server.emit("message", { data: "late" });
  assert.equal(upstream.sent.length, 1);
  assert.equal(rt.timers.size, 0);
  assert.equal(binding.calls.length, 1);
  assert.equal(rt.pairs.length, 1);
});

test("a peer close or error closes both ends once and clears the timer", async () => {
  let r = await relayedRegistration();
  r.upstream.emit("close", { code: 1008, reason: "refused" });
  assert.deepEqual(r.server.closed, { code: 1008, reason: "refused" });
  assert.equal(r.rt.timers.size, 0);
  r = await relayedRegistration();
  r.server.emit("error", {});
  assert.equal(r.upstream.closed.code, 1011);
  r.upstream.emit("close", { code: 1006, reason: "" });
  assert.equal(r.rt.timers.size, 0);
});

test("throwing bindings and failing streams leave no timer, socket or open reader", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const throwing = fakeBinding(() => { throw new Error("synthetic binding failure"); });
  assert.equal((await worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: INIT }), openEnv(throwing))).status, 502);
  assert.equal((await worker.fetch(request(MCP_PATH, { method: "DELETE", headers: { ...mcpHeaders, "Mcp-Session-Id": SESSION } }), openEnv(throwing))).status, 502);
  assert.equal((await worker.fetch(request(REGISTER_PATH, { method: "GET", headers: regHeaders }), openEnv(throwing))).status, 502);
  assert.equal(rt.timers.size, 0);

  const failingClient = trackedBody(["partial"], { fail: true });
  const unused = fakeBinding(() => json());
  assert.equal((await worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: failingClient.stream }), openEnv(unused))).status, 502);
  assert.equal(unused.calls.length, 0);

  const failingReply = trackedBody(["partial"], { fail: true });
  const replying = fakeBinding(() => ({ status: 200, headers: new Headers({ "Content-Type": "application/json" }), body: failingReply.stream }));
  assert.equal((await worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: INIT }), openEnv(replying))).status, 502);

  const odd = trackedBody([]);
  odd.stream.getReader = () => ({ read: async () => ({ done: false, value: "not bytes" }), cancel: async () => { odd.state.cancelled = true; } });
  assert.equal((await worker.fetch(request(MCP_PATH, { headers: mcpHeaders, body: odd.stream }), openEnv(unused))).status, 502);
  assert.ok(odd.state.cancelled);
  assert.equal(rt.timers.size, 0);
  assert.equal(rt.pairs.length, 0);
});

test("the source imports only the probe relay and has no global fetch, logging or CORS handling", () => {
  const source = readFileSync(new URL("./worker.mjs", import.meta.url), "utf8");
  assert.deepEqual(source.match(/^import .*$/gm), ['import { relay } from "../probe/worker.mjs";']);
  assert.deepEqual([...source.matchAll(/([\w.]*)fetch\(/g)].map((m) => m[1]), ["", "binding.", "binding.", "binding.", "binding.", "binding.", "binding.", "binding."]);
  assert.doesNotMatch(source, /console\.|globalThis\.fetch|Access-Control|caches\.|\.connect\(/);
  assert.ok(!source.includes("tether-echo-probe.internal"));
});

test("resident control requires explicit opt-in and keeps the bounded relay", async () => {
  const protocol = "rescue-agent-resident.v1";
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const { upstream, respond } = upgraded(protocol);
  const binding = fakeBinding(respond);
  const req = () => request(REGISTER_PATH, { method: "GET", headers: { ...regHeaders, "Sec-WebSocket-Protocol": protocol } });
  for (const flag of [undefined, "", "true", "open-resident-tether-control "]) {
    assert.equal((await worker.fetch(req(), { ...openEnv(binding), TETHER_RESIDENT_GATE: flag })).status, 400);
  }
  assert.equal(binding.calls.length, 0);
  const env = { ...openEnv(binding), TETHER_RESIDENT_GATE: "open-resident-tether-control" };
  assert.equal((await worker.fetch(req(), { ...env, TETHER_NATIVE_GATE: undefined })).status, 404);
  const result = await worker.fetch(req(), env);
  assert.equal(result.status, 101);
  assert.equal(result.headers.get("Sec-WebSocket-Protocol"), protocol);
  assert.deepEqual(binding.calls[0].init.headers, { Upgrade: "websocket", "Sec-WebSocket-Protocol": protocol });
  assert.equal(binding.calls[0].url, UPSTREAM_ORIGIN + REGISTER_PATH);
  const server = rt.pairs[0][1];
  upstream.emit("message", { data: "synthetic resident challenge" });
  assert.deepEqual(server.sent, ["synthetic resident challenge"]);
  assert.deepEqual([...rt.timers.values()].map(t => t.ms), [300_000]);
  rt.fireAll();
  assert.equal(server.closed.code, 4000);
  assert.equal(upstream.closed.code, 4000);
  assert.equal(rt.timers.size, 0);
  assert.equal(binding.calls.length, 1); // Worker never reconnects itself.
});

test("resident opt-in preserves forbidden headers and exact request/reply protocols", async () => {
  const protocol = "rescue-agent-resident.v1";
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(upgraded(protocol).respond);
  const env = { ...openEnv(binding), TETHER_RESIDENT_GATE: "open-resident-tether-control" };
  for (const header of ["Cookie", "Authorization", "Proxy-Authorization", ASSERTION_HEADER, "Origin"]) {
    const r = request(REGISTER_PATH, { method: "GET", headers: { ...regHeaders, "Sec-WebSocket-Protocol": protocol, [header]: "synthetic" } });
    assert.equal((await worker.fetch(r, env)).status, 403);
  }
  for (const value of [`${protocol}, ${SUBPROTOCOL}`, `${protocol}, ${protocol}`, "rescue-agent-resident.v2"]) {
    assert.equal((await worker.fetch(request(REGISTER_PATH, { method: "GET", headers: { ...regHeaders, "Sec-WebSocket-Protocol": value } }), env)).status, 400);
  }
  assert.equal(binding.calls.length, 0);
  for (const [offered, answered] of [[protocol, SUBPROTOCOL], [SUBPROTOCOL, protocol]]) {
    const mismatch = upgraded(answered);
    const r = await worker.fetch(request(REGISTER_PATH, { method: "GET", headers: { ...regHeaders, "Sec-WebSocket-Protocol": offered } }), { ...env, TETHER_NATIVE_VPC: fakeBinding(mismatch.respond) });
    assert.equal(r.status, 502);
    assert.equal(mismatch.upstream.closed.code, 1002);
  }
  assert.equal(rt.pairs.length, 0);
  assert.equal(rt.timers.size, 0);
});

// Host pairing (#391). The path and the 4 KiB body bound are the Go
// gateway's (agentgateway.PairPath, MaxPairBody); they are spelled out here so
// a drift on either side fails this file instead of a pilot.
const PAIR_PATH = "/tether-agent/v1/pair";
const PAIRING_GATE = "open-native-tether-pairing";
const PAIR_BODY_LIMIT = 4 * 1024;
const PAIR_TOKEN = "Bearer synthetic.pair.token";
const PAIR_BODY = '{"host_public_key":"ssh-ed25519 AAAAsynthetic","proof":"c3ludGhldGlj"}';
const pairHeaders = { Authorization: PAIR_TOKEN, "Content-Type": "application/json" };
const pairEnv = (binding) => ({ ...openEnv(binding), TETHER_NATIVE_PAIRING: PAIRING_GATE });
const pairReply = (status = 200, extra = {}, body = '{"registration_endpoint":"wss://tether-native.example.invalid/tether-agent/v1/register","host_label":"mac-1"}') =>
  new Response(body, { status, headers: { "Content-Type": "application/json", ...extra } });
const pairRefusal = (status, extra = {}) => new Response("\n", { status, headers: { "Content-Type": "text/plain; charset=utf-8", ...extra } });

test("pairing exports the gateway's fixed path and an explicit opt-in value", () => {
  assert.equal(native.PAIR_PATH, PAIR_PATH);
  assert.equal(native.PAIRING_GATE_VALUE, PAIRING_GATE);
  assert.notEqual(native.PAIRING_GATE_VALUE, GATE_VALUE);
  assert.notEqual(native.PAIRING_GATE_VALUE, native.RESIDENT_GATE_VALUE);
});

test("B2: without the explicit pairing opt-in the pair path stays 404 and nothing is read or forwarded", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => pairReply());
  for (const flag of [undefined, "", "true", PAIRING_GATE + " ", GATE_VALUE, native.RESIDENT_GATE_VALUE]) {
    const { stream, state } = trackedBody([PAIR_BODY]);
    const r = await worker.fetch(request(PAIR_PATH, { headers: pairHeaders, body: stream }), { ...openEnv(binding), TETHER_NATIVE_PAIRING: flag });
    assert.equal(r.status, 404, String(flag));
    assert.equal(state.pulls, 0, String(flag));
  }
  // The opt-in never opens the Worker by itself: the native gate still applies.
  assert.equal((await worker.fetch(request(PAIR_PATH, { headers: pairHeaders, body: PAIR_BODY }), { ...pairEnv(binding), TETHER_NATIVE_GATE: undefined })).status, 404);
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
});

test("B1: pairing forwards one POST with its bearer and bounded JSON body to the fixed upstream path, and nothing else", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => pairReply());
  const spoof = { Cookie: "s=synthetic", [ASSERTION_HEADER]: TOKEN, "Proxy-Authorization": "Basic c3ludGhldGlj", "X-Forwarded-For": "192.0.2.1", Host: "elsewhere.invalid" };
  const r = await worker.fetch(request(PAIR_PATH, { headers: { ...pairHeaders, ...spoof }, body: PAIR_BODY }), pairEnv(binding));
  assert.equal(r.status, 200);
  assert.equal(r.headers.get("Content-Type"), "application/json");
  assert.equal(r.headers.get("Cache-Control"), "no-store");
  assert.match(text(r), /"host_label":"mac-1"/);
  assert.equal(binding.calls.length, 1);
  const { url, init } = binding.calls[0];
  assert.equal(url, UPSTREAM_ORIGIN + PAIR_PATH);
  assert.equal(init.method, "POST");
  assert.equal(init.redirect, "manual");
  // The bearer reaches the gateway, which verifies it; ambient and identity
  // headers do not.
  assert.deepEqual(init.headers, pairHeaders);
  assert.equal(new TextDecoder().decode(init.body), PAIR_BODY);
  assert.equal(rt.timers.size, 0);
});

test("B1: the pair path answers only POST at its exact spelling; other methods are 405 and aliases or queries are refused", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => pairReply());
  for (const method of ["GET", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"]) {
    const r = await worker.fetch(request(PAIR_PATH, { method, headers: pairHeaders }), pairEnv(binding));
    assert.equal(r.status, 405, method);
    assert.equal(r.headers.get("Allow"), "POST", method);
  }
  assert.equal((await worker.fetch(request(PAIR_PATH, { headers: { ...pairHeaders, Upgrade: "websocket" }, body: PAIR_BODY }), pairEnv(binding))).status, 405);
  for (const path of [PAIR_PATH + "/", "/tether-agent/v1/Pair", "/x/../tether-agent/v1/pair", "/tether-agent/v1/%70air"]) {
    assert.equal((await worker.fetch(request(path, { headers: pairHeaders, body: PAIR_BODY }), pairEnv(binding))).status, 404, path);
  }
  assert.equal((await worker.fetch(request(PAIR_PATH + "?token=synthetic", { headers: pairHeaders, body: PAIR_BODY }), pairEnv(binding))).status, 400);
  assert.equal((await worker.fetch(request(PAIR_PATH, { headers: { ...pairHeaders, Origin: ORIGIN }, body: PAIR_BODY }), pairEnv(binding))).status, 403);
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
});

test("B1: pairing needs exactly one bounded bearer credential and a bounded Content-Type before forwarding", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => pairReply());
  for (const authorization of [null, "", `${PAIR_TOKEN}, ${PAIR_TOKEN}`, "Bearer " + "x".repeat(LIMITS.authorization)]) {
    const headers = { "Content-Type": "application/json", ...(authorization === null ? {} : { Authorization: authorization }) };
    assert.equal((await worker.fetch(request(PAIR_PATH, { headers, body: PAIR_BODY }), pairEnv(binding))).status, 403, String(authorization).slice(0, 20));
  }
  for (const type of ["application/json\u0001", "x".repeat(129)]) {
    assert.equal((await worker.fetch(request(PAIR_PATH, { headers: { ...pairHeaders, "Content-Type": type }, body: PAIR_BODY }), pairEnv(binding))).status, 400);
  }
  assert.equal(binding.calls.length, 0);
});

test("B1: pairing bodies are bounded at the gateway's 4 KiB and a stalled body times out with its reader cancelled", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => pairReply());
  assert.equal((await worker.fetch(request(PAIR_PATH, { headers: pairHeaders, body: "x".repeat(PAIR_BODY_LIMIT + 1) }), pairEnv(binding))).status, 413);
  for (const declared of ["999999999", "-1", "1e3", String(PAIR_BODY_LIMIT + 1)]) {
    assert.equal((await worker.fetch(request(PAIR_PATH, { headers: { ...pairHeaders, "Content-Length": declared }, body: "x" }), pairEnv(binding))).status, 413, declared);
  }
  assert.equal(binding.calls.length, 0);
  assert.equal((await worker.fetch(request(PAIR_PATH, { headers: pairHeaders, body: "x".repeat(PAIR_BODY_LIMIT) }), pairEnv(binding))).status, 200);
  assert.equal(binding.calls[0].init.body.byteLength, PAIR_BODY_LIMIT);

  const { stream, state } = trackedBody(["partial"], { hang: true });
  const pending = worker.fetch(request(PAIR_PATH, { headers: pairHeaders, body: stream }), pairEnv(binding));
  await until("body read pending", () => state.pulls === 2);
  assert.deepEqual([...rt.timers.values()].map((t) => t.ms), [LIMITS.bodyMs]);
  rt.fireAll();
  assert.equal((await pending).status, 408);
  await until("reader cancelled", () => state.cancelled);
  assert.equal(binding.calls.length, 1);
  assert.equal(rt.timers.size, 0);
});

test("B1: pairing relays only the gateway's own answers: its refusals and challenge survive, anything else is 502", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const send = async (respond) => worker.fetch(request(PAIR_PATH, { headers: pairHeaders, body: PAIR_BODY }), pairEnv(fakeBinding(respond)));
  for (const status of [400, 403, 404, 409, 413, 503]) {
    const r = await send(() => pairRefusal(status, { "Set-Cookie": "s=synthetic", Location: "https://elsewhere.invalid/" }));
    assert.equal(r.status, status, String(status));
    assert.equal(r.headers.get("Set-Cookie"), null);
    assert.equal(r.headers.get("Location"), null);
  }
  for (const challenge of ["Bearer", 'Bearer error="invalid_token"']) {
    const r = await send(() => pairRefusal(401, { "WWW-Authenticate": challenge }));
    assert.equal(r.status, 401);
    assert.equal(r.headers.get("WWW-Authenticate"), challenge);
  }
  const allow = await send(() => pairRefusal(405, { Allow: "POST" }));
  assert.equal(allow.status, 405);
  assert.equal(allow.headers.get("Allow"), "POST");
  for (const [what, respond] of [
    ["redirect", () => new Response(null, { status: 302, headers: { Location: "https://elsewhere.invalid/" } })],
    ["foreign challenge", () => pairRefusal(401, { "WWW-Authenticate": 'Bearer resource_metadata="https://elsewhere.invalid"' })],
    ["challenge without one", () => pairRefusal(401)],
    ["foreign Allow", () => pairRefusal(405, { Allow: "GET" })],
    ["unknown status", () => pairRefusal(500)],
    ["foreign media", () => new Response("<html>", { status: 200, headers: { "Content-Type": "text/html" } })],
    ["body without media", () => ({ status: 200, headers: new Headers(), body: new Response("x").body })],
    ["oversized reply", () => pairReply(200, {}, "y".repeat(LIMITS.reply + 1))],
  ]) {
    assert.equal((await send(respond)).status, 502, what);
  }
  assert.equal(rt.timers.size, 0);
});

test("B1: the pairing upstream deadline is 35 s: a hung call aborts and its late response is cancelled", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  let aborted = false;
  const hang = fakeBinding((_, init) => new Promise((_, reject) => init.signal.addEventListener("abort", () => { aborted = true; reject(new Error("aborted")); })));
  let pending = worker.fetch(request(PAIR_PATH, { headers: pairHeaders, body: PAIR_BODY }), pairEnv(hang));
  await until("upstream call", () => hang.calls.length === 1);
  assert.deepEqual([...rt.timers.values()].map((t) => t.ms), [LIMITS.upstreamMs]);
  rt.fireAll();
  assert.equal((await pending).status, 504);
  assert.ok(aborted);

  const late = deferred();
  const ignoring = fakeBinding(() => late.promise);
  pending = worker.fetch(request(PAIR_PATH, { headers: pairHeaders, body: PAIR_BODY }), pairEnv(ignoring));
  await until("upstream call", () => ignoring.calls.length === 1);
  rt.fireAll();
  assert.equal((await pending).status, 504);
  const lateBody = trackedBody(['{"late":true}']);
  late.resolve({ status: 200, headers: new Headers({ "Content-Type": "application/json" }), body: lateBody.stream });
  await until("late body cancelled", () => lateBody.state.cancelled);
  assert.equal(lateBody.state.pulls, 0);
  assert.equal(rt.timers.size, 0);
});

test("B3: opting into pairing leaves registration and MCP credential handling unchanged", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => json());
  for (const header of ["Cookie", "Authorization", "Proxy-Authorization", ASSERTION_HEADER]) {
    const r = await worker.fetch(request(REGISTER_PATH, { method: "GET", headers: { ...regHeaders, [header]: "synthetic" } }), pairEnv(binding));
    assert.equal(r.status, 403, header);
  }
  // Default (Access) mode MCP still demands the assertion; a pairing bearer is not one.
  assert.equal((await worker.fetch(request(MCP_PATH, { headers: { ...pairHeaders, Accept: "application/json, text/event-stream" }, body: INIT }), pairEnv(binding))).status, 403);
  assert.equal(binding.calls.length, 0);
  // The registration upgrade is not accepted at the pair path either.
  assert.equal((await worker.fetch(request(PAIR_PATH, { method: "GET", headers: regHeaders }), pairEnv(binding))).status, 405);
  assert.equal(binding.calls.length, 0);
});

test("B1: every pairing answer carries the gateway's media type, and Allow or a challenge only on their own status", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const send = async (respond) => worker.fetch(request(PAIR_PATH, { headers: pairHeaders, body: PAIR_BODY }), pairEnv(fakeBinding(respond)));
  // The gateway always names its media type; an empty, untyped answer is not
  // a pairing result the host could trust.
  for (const status of [200, 409]) {
    assert.equal((await send(() => ({ status, headers: new Headers(), body: null }))).status, 502, String(status));
  }
  const ok = await send(() => pairReply(200, { "WWW-Authenticate": "Bearer", Allow: "POST" }));
  assert.equal(ok.status, 200);
  assert.equal(ok.headers.get("WWW-Authenticate"), null);
  assert.equal(ok.headers.get("Allow"), null);
  const conflict = await send(() => pairRefusal(409, { Allow: "POST", "WWW-Authenticate": 'Bearer error="invalid_token"' }));
  assert.equal(conflict.status, 409);
  assert.equal(conflict.headers.get("Allow"), null);
  assert.equal(conflict.headers.get("WWW-Authenticate"), null);
  const unauthorized = await send(() => pairRefusal(401, { "WWW-Authenticate": "Bearer", Allow: "POST" }));
  assert.equal(unauthorized.headers.get("Allow"), null);
  const method = await send(() => pairRefusal(405, { Allow: "POST", "WWW-Authenticate": "Bearer" }));
  assert.equal(method.headers.get("WWW-Authenticate"), null);
  assert.equal(rt.timers.size, 0);
});

// Two-step host pairing (#453). These paths remain behind the existing native
// and pairing gates; the gateway owns consent state and registration results.
const PAIR_OPERATIONS_PATH = `${PAIR_PATH}/operations`;
const PAIR_CONFIRM_PATH = `${PAIR_PATH}/confirm`;
const PAIR_CONSENT_PATH = `${PAIR_PATH}/consent`;
const PAIR_CONSENT_CALLBACK_PATH = `${PAIR_CONSENT_PATH}/callback`;
const PAIR_STAGE_PATHS = [PAIR_OPERATIONS_PATH, PAIR_CONFIRM_PATH, PAIR_CONSENT_PATH, PAIR_CONSENT_CALLBACK_PATH];
const PAIR_CONFIRM_BODY = '{"operation":"synthetic-operation","proof":"c3ludGhldGlj"}';
const PAIR_CONSENT_COOKIE = "__Host-tether-pair-consent=synthetic-cookie";
const PAIR_FORM_BODY = "operation=synthetic-operation&form_token=synthetic-token&decision=confirm";
const pairConsentHeaders = { Cookie: PAIR_CONSENT_COOKIE, "Content-Type": "application/x-www-form-urlencoded" };

function pairStageRequest(path, method) {
  if (method === "GET") {
    if (path === PAIR_CONSENT_PATH) return request(`${path}?operation=synthetic-operation`, { method });
    if (path === PAIR_CONSENT_CALLBACK_PATH) return request(`${path}?code=synthetic-code&state=synthetic-state`, { method });
  }
  const headers = path === PAIR_CONSENT_PATH && method === "POST"
    ? pairConsentHeaders
    : { Authorization: PAIR_TOKEN, "Content-Type": "application/json" };
  const body = path === PAIR_CONSENT_PATH && method === "POST" ? PAIR_FORM_BODY : PAIR_CONFIRM_BODY;
  return request(path, { method, headers, body });
}

test("two-step pairing stays behind both gates and accepts only each exact path and method — why: aliases or an accidental route must never reach the gateway", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => new Response("ok", { headers: { "Content-Type": "text/plain" } }));
  const cases = [
    [PAIR_OPERATIONS_PATH, ["POST"]], [PAIR_CONFIRM_PATH, ["POST"]],
    [PAIR_CONSENT_PATH, ["GET", "POST"]], [PAIR_CONSENT_CALLBACK_PATH, ["GET"]],
  ];
  for (const [path, methods] of cases) {
    for (const method of methods) {
      const req = pairStageRequest(path, method);
      for (const env of [openEnv(binding), { ...pairEnv(binding), TETHER_NATIVE_PAIRING: "true" }, { ...pairEnv(binding), TETHER_NATIVE_GATE: undefined }]) {
        assert.equal((await worker.fetch(req, env)).status, 404, `${method} ${path}`);
      }
    }
    const suffix = path === PAIR_CONSENT_PATH ? "?operation=synthetic-operation" : path === PAIR_CONSENT_CALLBACK_PATH ? "?code=synthetic-code&state=synthetic-state" : "";
    const aliases = [path + "/" + suffix, path.toUpperCase() + suffix, path.replace("/pair", "/%70air") + suffix, `/x/..${path}${suffix}`];
    for (const alias of aliases) assert.equal((await worker.fetch(request(alias, { method: methods[0] }), pairEnv(binding))).status, 404, alias);
    for (const method of ["PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"]) {
      if (methods.includes(method)) continue;
      assert.equal((await worker.fetch(pairStageRequest(path, method), pairEnv(binding))).status, 405, `${method} ${path}`);
    }
    for (const method of methods) {
      const upgraded = pairStageRequest(path, method);
      upgraded.headers.set("Upgrade", "websocket");
      assert.equal((await worker.fetch(upgraded, pairEnv(binding))).status, 405, `Upgrade ${method} ${path}`);
    }
  }
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
});

test("two-step consent accepts only the operation and callback query allowlists — why: extra or duplicate values could change which consent the browser binds", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => new Response("ok", { headers: { "Content-Type": "text/html" } }));
  for (const query of ["", "?", "?token=x", "?operation=", "?operation=a&operation=b", "?operation=a&extra=b"]) {
    assert.equal((await worker.fetch(request(PAIR_CONSENT_PATH + query, { method: "GET" }), pairEnv(binding))).status, 400, query);
  }
  for (const query of ["", "?", "?state=s", "?code=c", "?code=c&state=s&extra=x", "?code=c&state=s&state=t", "?error=e&state=s", "?code=c&error=e&error_description=d&state=s"]) {
    assert.equal((await worker.fetch(request(PAIR_CONSENT_CALLBACK_PATH + query, { method: "GET" }), pairEnv(binding))).status, 400, query);
  }
  for (const query of ["?operation=op-1", "?operation=op-1&operation=op-2"]) {
    const response = await worker.fetch(request(PAIR_OPERATIONS_PATH + query, { headers: pairHeaders, body: PAIR_BODY }), pairEnv(binding));
    assert.equal(response.status, 400, query);
  }
  const postWithQuery = await worker.fetch(request(`${PAIR_CONSENT_PATH}?operation=op-1`, { headers: { ...pairConsentHeaders, Origin: ORIGIN }, body: PAIR_FORM_BODY }), pairEnv(binding));
  assert.equal(postWithQuery.status, 400);
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
});

test("consent callback accepts the IdP's complete error response tuple — why: denial must reach the gateway so it can clear the one-time flow", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const query = "?error=access_denied&error_description=No+consent&state=synthetic-state";
  const binding = fakeBinding(() => new Response("denied", { status: 400, headers: { "Content-Type": "text/html" } }));
  const response = await worker.fetch(request(PAIR_CONSENT_CALLBACK_PATH + query, { method: "GET" }), pairEnv(binding));
  assert.equal(response.status, 400);
  assert.equal(binding.calls[0].url, `${UPSTREAM_ORIGIN}${PAIR_CONSENT_CALLBACK_PATH}${query}`);
  assert.equal(rt.timers.size, 0);
});

test("operations forwards one bearer while confirm strips credentials and both retain gateway status and body — why: only the precheck authenticates and confirm outcomes must stay distinguishable", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const spoof = { Cookie: PAIR_CONSENT_COOKIE, "Proxy-Authorization": "Basic synthetic", [ASSERTION_HEADER]: TOKEN, "X-Forwarded-For": "192.0.2.1" };
  const operationReply = '{"operation":"op-1","consent_url":"https://tether-native.example.invalid/consent"}';
  const operationBinding = fakeBinding(() => new Response(operationReply, { status: 200, headers: { "Content-Type": "application/json" } }));
  const operation = await worker.fetch(request(PAIR_OPERATIONS_PATH, { headers: { ...pairHeaders, ...spoof }, body: PAIR_BODY }), pairEnv(operationBinding));
  assert.equal(operation.status, 200);
  assert.equal(text(operation), operationReply);
  assert.equal(operationBinding.calls[0].url, UPSTREAM_ORIGIN + PAIR_OPERATIONS_PATH);
  assert.deepEqual(operationBinding.calls[0].init.headers, pairHeaders);
  assert.equal(operationBinding.calls[0].init.redirect, "manual");
  for (const authorization of [null, "", `${PAIR_TOKEN}, ${PAIR_TOKEN}`]) {
    const headers = { ...pairHeaders, ...spoof };
    if (authorization === null) delete headers.Authorization;
    else headers.Authorization = authorization;
    const refused = await worker.fetch(request(PAIR_OPERATIONS_PATH, { headers, body: PAIR_BODY }), pairEnv(operationBinding));
    assert.equal(refused.status, 403, String(authorization));
  }
  const unknownBody = '{"error":"registration_unknown"}';
  const unknownBinding = fakeBinding(() => new Response(unknownBody, { status: 500, headers: { "Content-Type": "application/json" } }));
  const unknown = await worker.fetch(request(PAIR_OPERATIONS_PATH, { headers: pairHeaders, body: PAIR_BODY }), pairEnv(unknownBinding));
  assert.equal(unknown.status, 500);
  assert.equal(text(unknown), unknownBody);

  for (const status of [500, 202, 403, 404, 409, 503]) {
    const body = `{"status":${status},"error":"registration_unknown"}`;
    const binding = fakeBinding(() => new Response(body, { status, headers: { "Content-Type": "application/json" } }));
    const response = await worker.fetch(request(PAIR_CONFIRM_PATH, { headers: { ...pairHeaders, ...spoof }, body: PAIR_CONFIRM_BODY }), pairEnv(binding));
    assert.equal(response.status, status, String(status));
    assert.equal(text(response), body, String(status));
    assert.deepEqual(binding.calls[0].init.headers, { "Content-Type": "application/json" });
  }
  assert.equal(rt.timers.size, 0);
});

test("two-step JSON and form requests enforce their media type and 4 KiB body bound — why: the gateway parsers receive only the intended small wire formats", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => new Response("ok", { headers: { "Content-Type": "text/plain" } }));
  const posts = [
    [PAIR_OPERATIONS_PATH, { ...pairHeaders }, PAIR_BODY],
    [PAIR_CONFIRM_PATH, { "Content-Type": "application/json" }, PAIR_CONFIRM_BODY],
    [PAIR_CONSENT_PATH, { ...pairConsentHeaders, Origin: ORIGIN }, PAIR_FORM_BODY],
  ];
  for (const [path, headers, body] of posts) {
    const over = "x".repeat(PAIR_BODY_LIMIT + 1);
    assert.equal((await worker.fetch(request(path, { headers, body: over }), pairEnv(binding))).status, 413, path);
    assert.equal((await worker.fetch(request(path, { headers: { ...headers, "Content-Length": String(PAIR_BODY_LIMIT + 1) }, body: "x" }), pairEnv(binding))).status, 413, path);
    assert.equal((await worker.fetch(request(path, { headers: { ...headers, "Content-Type": "text/plain" }, body }), pairEnv(binding))).status, 400, path);
    assert.equal((await worker.fetch(request(path, { headers, body: "x".repeat(PAIR_BODY_LIMIT) }), pairEnv(binding))).status, 200, path);
  }
  assert.equal(binding.calls.length, 3);
  for (const call of binding.calls) assert.equal(call.init.body.byteLength, PAIR_BODY_LIMIT);
  assert.equal(rt.timers.size, 0);
});

test("consent redirects and confirmation pages preserve the gateway's redirect, cookie, cache and frame protections — why: the browser must enforce the gateway's one-time consent boundary", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const location = "https://idp.example.invalid/authorize?client_id=synthetic&state=state-1";
  const cookie = "__Host-tether-pair-consent=synthetic; Secure; HttpOnly; SameSite=Lax; Path=/";
  const security = {
    "Cache-Control": "no-store", "X-Frame-Options": "DENY",
    "Content-Security-Policy": "frame-ancestors 'none'; default-src 'none'; form-action 'self'",
    "Content-Type": "text/html; charset=utf-8",
  };
  const redirectBinding = fakeBinding(() => new Response(null, { status: 302, headers: { ...security, Location: location, "Set-Cookie": cookie } }));
  const redirect = await worker.fetch(request(`${PAIR_CONSENT_PATH}?operation=synthetic-operation`, {
    method: "GET", headers: { Authorization: PAIR_TOKEN, "Proxy-Authorization": "Basic synthetic", [ASSERTION_HEADER]: TOKEN, Cookie: PAIR_CONSENT_COOKIE },
  }), pairEnv(redirectBinding));
  assert.equal(redirect.status, 302);
  for (const [name, value] of Object.entries({ ...security, Location: location, "Set-Cookie": cookie })) assert.equal(redirect.headers.get(name), value, name);
  assert.equal(redirectBinding.calls[0].init.redirect, "manual");
  assert.equal(redirectBinding.calls[0].url, `${UPSTREAM_ORIGIN}${PAIR_CONSENT_PATH}?operation=synthetic-operation`);
  assert.deepEqual(redirectBinding.calls[0].init.headers, {});

  const pageBody = "<!doctype html><form>Confirm this Mac</form>";
  const pageBinding = fakeBinding(() => new Response(pageBody, { status: 200, headers: { ...security, "Set-Cookie": cookie } }));
  const page = await worker.fetch(request(`${PAIR_CONSENT_CALLBACK_PATH}?code=synthetic-code&state=synthetic-state`, { method: "GET" }), pairEnv(pageBinding));
  assert.equal(page.status, 200);
  assert.equal(text(page), pageBody);
  for (const [name, value] of Object.entries({ ...security, "Set-Cookie": cookie })) assert.equal(page.headers.get(name), value, name);
  assert.equal(rt.timers.size, 0);
});

test("browser consent forwards only its cookie and form while dropping all ambient credentials — why: IdP consent must not inherit Worker or Access identity", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const spoof = { Authorization: PAIR_TOKEN, "Proxy-Authorization": "Basic synthetic", [ASSERTION_HEADER]: TOKEN, "X-Forwarded-For": "192.0.2.1" };
  const callbackBinding = fakeBinding(() => new Response("page", { headers: { "Content-Type": "text/html" } }));
  const callback = await worker.fetch(request(`${PAIR_CONSENT_CALLBACK_PATH}?code=c&state=s`, { method: "GET", headers: { ...spoof, Cookie: PAIR_CONSENT_COOKIE } }), pairEnv(callbackBinding));
  assert.equal(callback.status, 200);
  assert.deepEqual(callbackBinding.calls[0].init.headers, { Cookie: PAIR_CONSENT_COOKIE });
  assert.equal(callbackBinding.calls[0].url, `${UPSTREAM_ORIGIN}${PAIR_CONSENT_CALLBACK_PATH}?code=c&state=s`);

  const consentBinding = fakeBinding(() => new Response("done", { headers: { "Content-Type": "text/html" } }));
  const consent = await worker.fetch(request(PAIR_CONSENT_PATH, {
    headers: { ...pairConsentHeaders, ...spoof, Origin: ORIGIN, "X-Request-Id": "synthetic" }, body: PAIR_FORM_BODY,
  }), pairEnv(consentBinding));
  assert.equal(consent.status, 200);
  assert.deepEqual(consentBinding.calls[0].init.headers, pairConsentHeaders);
  assert.equal(consentBinding.calls[0].init.method, "POST");
  assert.equal(new TextDecoder().decode(consentBinding.calls[0].init.body), PAIR_FORM_BODY);
  assert.equal(rt.timers.size, 0);
});

test("consent POST requires the request's public origin — why: a cross-origin form must not consume the browser-bound form token", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(() => new Response("done"));
  for (const origin of [undefined, "https://attacker.example.invalid", `${ORIGIN}/path`, `${ORIGIN}:443`]) {
    const headers = { ...pairConsentHeaders, ...(origin === undefined ? {} : { Origin: origin }) };
    assert.equal((await worker.fetch(request(PAIR_CONSENT_PATH, { headers, body: PAIR_FORM_BODY }), pairEnv(binding))).status, 403, String(origin));
  }
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
});

// The rescue reception route (#422, docs/rescue-connection-start.md 受付の登録):
// an opt-in like pairing, and as credential free as registration.
const rescueHeaders = () => ({ Upgrade: "websocket", "Sec-WebSocket-Protocol": native.RESCUE_SUBPROTOCOL });
const rescueEnv = (binding) => ({ ...openEnv(binding), TETHER_NATIVE_RESCUE: native.RESCUE_GATE_VALUE });

test("the rescue reception forwards only the fixed upgrade and subprotocol; the Worker adds no credential and sends nothing", async () => {
  assert.equal(native.RESCUE_PATH, "/tether-agent/v1/rescue");
  assert.equal(native.RESCUE_SUBPROTOCOL, "rescue-agent-reception.v1");
  const rt = fakeRuntime();
  const { upstream, respond } = upgraded(native.RESCUE_SUBPROTOCOL);
  const binding = fakeBinding(respond);
  const r = await createNativeWorker(rt).fetch(request(native.RESCUE_PATH, {
    method: "GET", headers: { ...rescueHeaders(), "X-Forwarded-For": "192.0.2.1", "Sec-WebSocket-Extensions": "permessage-deflate" },
  }), rescueEnv(binding));
  assert.equal(r.status, 101);
  assert.equal(r.headers.get("Sec-WebSocket-Protocol"), native.RESCUE_SUBPROTOCOL);
  const [client, server] = rt.pairs[0];
  assert.equal(r.webSocket, client);
  const [{ url, init }] = binding.calls;
  assert.equal(url, UPSTREAM_ORIGIN + native.RESCUE_PATH);
  assert.deepEqual(init.headers, { Upgrade: "websocket", "Sec-WebSocket-Protocol": native.RESCUE_SUBPROTOCOL });
  assert.equal(init.method, "GET");
  assert.equal(init.redirect, "manual");
  // The nonces, K's proof and the signed result are Go's and the host's.
  assert.deepEqual(server.sent, []);
  assert.deepEqual(upstream.sent, []);
});

test("the rescue reception is closed unless the deployment opts in", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(upgraded("rescue-agent-reception.v1").respond);
  const headers = { Upgrade: "websocket", "Sec-WebSocket-Protocol": "rescue-agent-reception.v1" };
  for (const env of [openEnv(binding), { ...openEnv(binding), TETHER_NATIVE_RESCUE: "yes" }, { ...openEnv(binding), TETHER_NATIVE_PAIRING: native.PAIRING_GATE_VALUE }]) {
    assert.equal((await worker.fetch(request("/tether-agent/v1/rescue", { method: "GET", headers }), env)).status, 404);
  }
  assert.equal(binding.calls.length, 0);
  assert.equal((await worker.fetch(request("/tether-agent/v1/rescue", { method: "GET", headers }), rescueEnv(binding))).status, 101);
});

test("the rescue reception refuses cookies, authorization, Access assertions and any other subprotocol before forwarding", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const binding = fakeBinding(upgraded(native.RESCUE_SUBPROTOCOL).respond);
  for (const extra of [{ Cookie: "CF_Authorization=synthetic" }, { Authorization: "Bearer synthetic" }, { "Proxy-Authorization": "Basic synthetic" },
    { [ASSERTION_HEADER]: TOKEN }]) {
    assert.equal((await worker.fetch(request(native.RESCUE_PATH, { method: "GET", headers: { ...rescueHeaders(), ...extra } }), rescueEnv(binding))).status, 403);
  }
  for (const protocol of [undefined, SUBPROTOCOL, native.RESIDENT_SUBPROTOCOL, `${native.RESCUE_SUBPROTOCOL}, other`]) {
    const headers = rescueHeaders();
    if (protocol === undefined) delete headers["Sec-WebSocket-Protocol"]; else headers["Sec-WebSocket-Protocol"] = protocol;
    assert.equal((await worker.fetch(request(native.RESCUE_PATH, { method: "GET", headers }), rescueEnv(binding))).status, 400);
  }
  assert.equal((await worker.fetch(request(native.RESCUE_PATH, { method: "POST", headers: rescueHeaders() }), rescueEnv(binding))).status, 405);
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.pairs.length, 0);
  assert.equal(rt.timers.size, 0);
});

test("the rescue reception relays the gateway's 429 and 503; other answers stay refusals", async () => {
  const rt = fakeRuntime();
  const worker = createNativeWorker(rt);
  const send = (respond) => worker.fetch(request(native.RESCUE_PATH, { method: "GET", headers: rescueHeaders() }), rescueEnv(fakeBinding(respond)));
  const rate = await send(() => new Response("Too Many Requests\n", { status: 429, headers: { "Retry-After": "60" } }));
  assert.equal(rate.status, 429);
  assert.equal(rate.headers.get("Retry-After"), "60");
  const busy = await send(() => new Response("Service Unavailable\n", { status: 503, headers: { "Retry-After": "1" } }));
  assert.equal(busy.status, 503);
  assert.equal(busy.headers.get("Retry-After"), "1");
  assert.equal((await send(() => new Response("ok", { status: 200 }))).status, 502);
  assert.equal((await send(upgraded(SUBPROTOCOL).respond)).status, 502);
  assert.equal(rt.pairs.length, 0);
  assert.equal(rt.timers.size, 0);
});

// #433: the reception and its channel share one WSS (docs/rescue-connection-start.md
// 流れ), so the rescue relay must outlast the longest session it carries: the
// reception handshake (agenttransport Registration, 10 s), a code's lifetime
// (5 min), the gateway's request expiry (75 s) and the longest grant (24 h).
const RESCUE_SPAN_MS = 10_000 + 5 * 60_000 + 75_000 + 24 * 3_600_000;
const RESCUE_LIFETIME_MS = 24 * 3_600_000 + 10 * 60_000;

async function relayedRescue(rt = fakeRuntime()) {
  const { upstream, respond } = upgraded(native.RESCUE_SUBPROTOCOL);
  const binding = fakeBinding(respond);
  const r = await createNativeWorker(rt).fetch(request(native.RESCUE_PATH, { method: "GET", headers: rescueHeaders() }), rescueEnv(binding));
  assert.equal(r.status, 101);
  return { rt, binding, server: rt.pairs.at(-1)[1], upstream };
}

test("the rescue relay outlasts the longest session with one absolute 24 h 10 min timer; registration and resident stay at 5 min", async () => {
  assert.ok(RESCUE_LIFETIME_MS > RESCUE_SPAN_MS);
  const { rt, binding, server, upstream } = await relayedRescue();
  assert.deepEqual([...rt.timers.values()].map((t) => t.ms), [RESCUE_LIFETIME_MS]);

  // The same Worker still gives registration and resident control 5 min.
  const env = { ...rescueEnv(fakeBinding(upgraded().respond)), TETHER_RESIDENT_GATE: native.RESIDENT_GATE_VALUE };
  const worker = createNativeWorker(rt);
  assert.equal((await worker.fetch(request(REGISTER_PATH, { method: "GET", headers: regHeaders }), env)).status, 101);
  env.TETHER_NATIVE_VPC = fakeBinding(upgraded(native.RESIDENT_SUBPROTOCOL).respond);
  assert.equal((await worker.fetch(request(REGISTER_PATH, { method: "GET", headers: { ...regHeaders, "Sec-WebSocket-Protocol": native.RESIDENT_SUBPROTOCOL } }), env)).status, 101);
  assert.deepEqual([...rt.timers.values()].map((t) => t.ms), [RESCUE_LIFETIME_MS, 5 * 60 * 1000, 5 * 60 * 1000]);

  // Ending the rescue lifetime closes both ends with 4000 and nothing reconnects.
  const [id] = rt.timers.keys();
  rt.timers.get(id).fn();
  assert.equal(server.closed.code, 4000);
  assert.equal(upstream.closed.code, 4000);
  assert.ok(!rt.timers.has(id));
  assert.equal(binding.calls.length, 1);
  assert.equal(rt.pairs.length, 3);
});

test("the rescue relay keeps registration's frame rate over its lifetime: a day of heartbeats passes and the budget stays finite", async () => {
  // Both channel ends beat every 10 s (agentgateway channel budgets) and answer
  // the other's beat, so each direction carries two messages per 10 s at least.
  const beats = 2 * Math.ceil(RESCUE_LIFETIME_MS / 10_000);
  const budget = LIMITS.frames * (RESCUE_LIFETIME_MS / LIMITS.lifetimeMs);
  assert.ok(Number.isInteger(budget) && beats < budget);
  const { rt, server, upstream } = await relayedRescue();
  let sent = 0;
  server.send = () => { sent++; };
  for (let i = 0; i < beats; i++) upstream.emit("message", { data: "f" });
  assert.equal(server.closed, null);
  for (let i = beats; i < budget; i++) upstream.emit("message", { data: "f" });
  assert.equal(server.closed, null);
  upstream.emit("message", { data: "f" });
  assert.equal(sent, budget);
  assert.equal(server.closed.code, 1008);
  assert.equal(upstream.closed.code, 1008);
  assert.equal(rt.timers.size, 0);
});

test("the rescue relay keeps registration's 32 KiB message limit; only the lifetime and frame budget differ", async () => {
  const { rt, server, upstream } = await relayedRescue();
  server.emit("message", { data: new Uint8Array(LIMITS.message).buffer });
  assert.equal(upstream.sent.length, 1);
  server.emit("message", { data: new Uint8Array(LIMITS.message + 1).buffer });
  assert.equal(upstream.sent.length, 1);
  assert.equal(upstream.closed.code, 1009);
  assert.equal(server.closed.code, 1009);
  assert.equal(rt.timers.size, 0);
});
