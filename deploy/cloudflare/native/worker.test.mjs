// node --test deploy/cloudflare/native/
// Fake binding, sockets and timers only: no network, Worker runtime,
// Cloudflare account or real assertion. Every value is synthetic.
import { test, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  createNativeWorker, MCP_PATH, REGISTER_PATH, SUBPROTOCOL, ASSERTION_HEADER, GATE_VALUE, UPSTREAM_ORIGIN, LIMITS,
} from "./worker.mjs";
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

test("the source imports only the probe relay and has no global fetch, logging, cookie or CORS handling", () => {
  const source = readFileSync(new URL("./worker.mjs", import.meta.url), "utf8");
  assert.deepEqual(source.match(/^import .*$/gm), ['import { relay } from "../probe/worker.mjs";']);
  assert.deepEqual([...source.matchAll(/([\w.]*)fetch\(/g)].map((m) => m[1]), ["", "binding.", "binding."]);
  assert.doesNotMatch(source, /console\.|globalThis\.fetch|Access-Control|"Set-Cookie"|caches\.|\.connect\(/);
  assert.ok(!source.includes("tether-echo-probe.internal"));
});
