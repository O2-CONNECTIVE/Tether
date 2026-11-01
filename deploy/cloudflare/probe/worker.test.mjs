// node --test deploy/cloudflare/probe/
// Fake binding, sockets and timers only: no network, Worker runtime,
// Cloudflare account or real assertion. Every value is synthetic.
import { test, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  createProbeWorker, relay, closeCode, HTTP_PATH, WS_PATH, SUBPROTOCOL, ASSERTION_HEADER, GATE_VALUE, UPSTREAM_ORIGIN, LIMITS,
} from "./worker.mjs";

const ORIGIN = "https://tether-dev.example.invalid";
const TOKEN = "synthetic.assertion.value";

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

const echoHeaders = { [ASSERTION_HEADER]: TOKEN, "Content-Type": "application/octet-stream" };
const wsHeaders = { [ASSERTION_HEADER]: TOKEN, Upgrade: "websocket", "Sec-WebSocket-Protocol": SUBPROTOCOL };
const openEnv = (binding) => ({ TETHER_PROBE_GATE: GATE_VALUE, TETHER_PROBE_VPC: binding });
const okUpstream = () => new Response('{"probe":"tether-echo-probe/v1","command_access":false}', { status: 200, headers: { "Content-Type": "application/json" } });
const text = (r) => new TextDecoder().decode(r.body);

let realFetch;
beforeEach(() => { realFetch = globalThis.fetch; globalThis.fetch = () => { throw new Error("global fetch must never be used"); }; });
afterEach(() => { globalThis.fetch = realFetch; });

test("deny all by default: no gate, wrong gate or missing binding forwards nothing", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(okUpstream);
  const worker = createProbeWorker(rt);
  for (const env of [{}, { TETHER_PROBE_VPC: binding }, { TETHER_PROBE_GATE: "true", TETHER_PROBE_VPC: binding }, { TETHER_PROBE_GATE: "closed", TETHER_PROBE_VPC: binding }, undefined]) {
    assert.equal((await worker.fetch(request(HTTP_PATH, { headers: echoHeaders, body: "x" }), env)).status, 404);
  }
  assert.equal((await worker.fetch(request(HTTP_PATH, { headers: echoHeaders, body: "x" }), { TETHER_PROBE_GATE: GATE_VALUE })).status, 503);
  assert.equal(binding.calls.length, 0);
});

test("a closed gate refuses upgrades and arbitrary paths with no read, timer, socket or forward", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(upgraded().respond);
  const worker = createProbeWorker(rt);
  const closed = { TETHER_PROBE_GATE: "closed", TETHER_PROBE_VPC: binding };
  const bodies = [];
  for (const [path, method, headers] of [
    [WS_PATH, "GET", wsHeaders], [WS_PATH, "GET", { ...wsHeaders, Origin: ORIGIN }], ["/", "GET", wsHeaders],
    ["/admin", "POST", echoHeaders], ["/tether-probe/v1/echo/../ws", "POST", echoHeaders], [HTTP_PATH + "?x=1", "PUT", echoHeaders],
    [HTTP_PATH, "POST", echoHeaders],
  ]) {
    const tracked = trackedBody(["synthetic"]);
    bodies.push(tracked.state);
    const r = await worker.fetch(request(path, { method, headers, body: method === "GET" ? null : tracked.stream }), closed);
    assert.equal(r.status, 404, `${method} ${path}`);
  }
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
  assert.equal(rt.pairs.length, 0);
  assert.ok(bodies.every((s) => s.pulls === 0 && !s.cancelled));
});

test("any Origin is refused before forwarding, for the echo and the upgrade", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(okUpstream);
  const worker = createProbeWorker(rt);
  for (const origin of [ORIGIN, "null", ""]) {
    assert.equal((await worker.fetch(request(HTTP_PATH, { headers: { ...echoHeaders, Origin: origin }, body: "x" }), openEnv(binding))).status, 403);
    assert.equal((await worker.fetch(request(WS_PATH, { method: "GET", headers: { ...wsHeaders, Origin: origin } }), openEnv(binding))).status, 403);
  }
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
  assert.equal(rt.pairs.length, 0);
});

test("only the exact probe paths and methods forward", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(okUpstream);
  const worker = createProbeWorker(rt);
  const cases = [
    ["/x/../tether-probe/v1/echo", "POST", echoHeaders, 404], ["/%2e/tether-probe/v1/echo", "POST", echoHeaders, 404],
    ["/", "POST", echoHeaders, 404], ["/mcp", "POST", echoHeaders, 404], [HTTP_PATH + "/x", "POST", echoHeaders, 404],
    [HTTP_PATH + "/", "POST", echoHeaders, 404], ["/tether-probe/v1/%65cho", "POST", echoHeaders, 404],
    [HTTP_PATH + "?target=elsewhere", "POST", echoHeaders, 400], [HTTP_PATH + "?", "POST", echoHeaders, 400],
    [HTTP_PATH, "GET", echoHeaders, 405], [HTTP_PATH, "POST", { ...echoHeaders, Upgrade: "websocket" }, 405],
    [WS_PATH, "POST", wsHeaders, 405], [WS_PATH, "GET", { [ASSERTION_HEADER]: TOKEN }, 405],
  ];
  for (const [path, method, headers, status] of cases) {
    const r = await worker.fetch(request(path, { method, headers, body: method === "POST" ? "x" : null }), openEnv(binding));
    assert.equal(r.status, status, `${method} ${path}`);
  }
  assert.equal(binding.calls.length, 0);
});

test("a missing or doubled assertion is refused early; presence alone is not authentication", async () => {
  const rt = fakeRuntime();
  const worker = createProbeWorker(rt);
  const none = fakeBinding(okUpstream);
  for (const headers of [{ "Content-Type": "application/octet-stream" }, { ...echoHeaders, [ASSERTION_HEADER]: `${TOKEN}, ${TOKEN}` },
    { ...echoHeaders, [ASSERTION_HEADER]: "a".repeat(LIMITS.assertion + 1) }]) {
    assert.equal((await worker.fetch(request(HTTP_PATH, { headers, body: "x" }), openEnv(none))).status, 403);
  }
  assert.equal(none.calls.length, 0);
  // A present but unverifiable assertion is forwarded and the backend's 403 stands.
  const backend = fakeBinding(() => new Response("the Cloudflare Access assertion is not valid\n", { status: 403 }));
  const r = await worker.fetch(request(HTTP_PATH, { headers: { ...echoHeaders, [ASSERTION_HEADER]: "synthetic-not-a-jwt" }, body: "x" }), openEnv(backend));
  assert.equal(r.status, 403);
  assert.equal(backend.calls.length, 1);
});

test("the HTTP echo forwards only the fixed URL, assertion, media type and bounded body", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(okUpstream);
  const r = await createProbeWorker(rt).fetch(request(HTTP_PATH, {
    headers: { ...echoHeaders, Cookie: "CF_Authorization=synthetic", Authorization: "Bearer synthetic", "X-Target": "http://elsewhere.invalid" },
    body: "synthetic-route",
  }), openEnv(binding));
  assert.equal(r.status, 200);
  assert.equal(r.headers.get("Content-Type"), "application/json");
  assert.match(text(r), /"command_access":false/);
  const [{ url, init }] = binding.calls;
  assert.equal(url, UPSTREAM_ORIGIN + HTTP_PATH);
  assert.deepEqual(init.headers, { [ASSERTION_HEADER]: TOKEN, "Content-Type": "application/octet-stream" });
  assert.equal(init.redirect, "manual");
  assert.equal(new TextDecoder().decode(init.body), "synthetic-route");
  assert.equal(rt.timers.size, 0);
});

test("oversized bodies and replies, redirects and timeouts are refused", async () => {
  const rt = fakeRuntime();
  const worker = createProbeWorker(rt);
  const binding = fakeBinding(okUpstream);
  const big = "x".repeat(LIMITS.body + 1);
  assert.equal((await worker.fetch(request(HTTP_PATH, { headers: echoHeaders, body: big }), openEnv(binding))).status, 413);
  assert.equal((await worker.fetch(request(HTTP_PATH, { headers: { ...echoHeaders, "Content-Length": "999999" }, body: "x" }), openEnv(binding))).status, 413);
  assert.equal((await worker.fetch(request(HTTP_PATH, { headers: { ...echoHeaders, "Content-Type": "text/plain" }, body: "x" }), openEnv(binding))).status, 415);
  assert.equal(binding.calls.length, 0);

  const redirect = fakeBinding(() => new Response(null, { status: 302, headers: { Location: "https://elsewhere.invalid/" } }));
  assert.equal((await worker.fetch(request(HTTP_PATH, { headers: echoHeaders, body: "x" }), openEnv(redirect))).status, 502);
  const huge = fakeBinding(() => new Response("y".repeat(LIMITS.reply + 1), { status: 200 }));
  assert.equal((await worker.fetch(request(HTTP_PATH, { headers: echoHeaders, body: "x" }), openEnv(huge))).status, 502);

  let aborted = false;
  const hang = fakeBinding((_, init) => new Promise((_, reject) => init.signal.addEventListener("abort", () => { aborted = true; reject(new Error("aborted")); })));
  const pending = worker.fetch(request(HTTP_PATH, { headers: echoHeaders, body: "x" }), openEnv(hang));
  await until("upstream call", () => hang.calls.length === 1);
  assert.equal(rt.timers.size, 1);
  assert.equal([...rt.timers.values()][0].ms, LIMITS.upstreamMs);
  rt.fireAll();
  assert.equal((await pending).status, 504);
  assert.ok(aborted);
  assert.equal(rt.timers.size, 0);
});

test("a stalled client body times out and its reader is cancelled", async () => {
  const rt = fakeRuntime();
  const binding = fakeBinding(okUpstream);
  const { stream, state } = trackedBody(["partial"], { hang: true });
  const pending = createProbeWorker(rt).fetch(request(HTTP_PATH, { headers: echoHeaders, body: stream }), openEnv(binding));
  await until("body read pending", () => state.pulls === 2);
  assert.equal(rt.timers.size, 1);
  assert.equal([...rt.timers.values()][0].ms, LIMITS.bodyMs);
  rt.fireAll();
  assert.equal((await pending).status, 408);
  await until("reader cancelled", () => state.cancelled);
  assert.equal(binding.calls.length, 0);
  assert.equal(rt.timers.size, 0);
});

test("a late HTTP response after the upstream deadline is cancelled, not read", async () => {
  const rt = fakeRuntime();
  const late = deferred();
  const binding = fakeBinding(() => late.promise); // ignores the abort
  const pending = createProbeWorker(rt).fetch(request(HTTP_PATH, { headers: echoHeaders, body: "x" }), openEnv(binding));
  await until("upstream call", () => binding.calls.length === 1);
  assert.equal([...rt.timers.values()][0].ms, LIMITS.upstreamMs);
  rt.fireAll();
  assert.equal((await pending).status, 504);
  const { stream, state } = trackedBody(['{"probe":"late"}']);
  late.resolve({ status: 200, headers: new Headers({ "Content-Type": "application/json" }), body: stream });
  await until("late body cancelled", () => state.cancelled);
  assert.equal(state.pulls, 0);
  assert.equal(rt.timers.size, 0);
});

test("a reply body that stalls past the upstream deadline is cancelled", async () => {
  const rt = fakeRuntime();
  const { stream, state } = trackedBody([], { hang: true });
  const binding = fakeBinding(() => ({ status: 200, headers: new Headers(), body: stream }));
  const pending = createProbeWorker(rt).fetch(request(HTTP_PATH, { headers: echoHeaders, body: "x" }), openEnv(binding));
  await until("reply read pending", () => state.pulls === 1);
  rt.fireAll();
  assert.equal((await pending).status, 504);
  await until("reply reader cancelled", () => state.cancelled);
  assert.equal(rt.timers.size, 0);
});

test("a late WebSocket upgrade after the upstream deadline is closed, never relayed", async () => {
  const rt = fakeRuntime();
  const late = deferred();
  const binding = fakeBinding(() => late.promise); // ignores the abort
  const pending = createProbeWorker(rt).fetch(request(WS_PATH, { method: "GET", headers: wsHeaders }), openEnv(binding));
  await until("upstream call", () => binding.calls.length === 1);
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
});

test("throwing bindings and failing streams leave no timer or open reader", async () => {
  const rt = fakeRuntime();
  const worker = createProbeWorker(rt);
  const throwing = fakeBinding(() => { throw new Error("synthetic binding failure"); });
  assert.equal((await worker.fetch(request(HTTP_PATH, { headers: echoHeaders, body: "x" }), openEnv(throwing))).status, 502);
  assert.equal((await worker.fetch(request(WS_PATH, { method: "GET", headers: wsHeaders }), openEnv(throwing))).status, 502);
  assert.equal(rt.timers.size, 0);

  const failingClient = trackedBody(["partial"], { fail: true });
  const unused = fakeBinding(okUpstream);
  assert.equal((await worker.fetch(request(HTTP_PATH, { headers: echoHeaders, body: failingClient.stream }), openEnv(unused))).status, 502);
  assert.equal(unused.calls.length, 0);
  assert.equal(rt.timers.size, 0);

  const failingReply = trackedBody(["partial"], { fail: true });
  const replying = fakeBinding(() => ({ status: 200, headers: new Headers(), body: failingReply.stream }));
  assert.equal((await worker.fetch(request(HTTP_PATH, { headers: echoHeaders, body: "x" }), openEnv(replying))).status, 502);
  assert.equal(rt.timers.size, 0);

  const odd = trackedBody([]);
  odd.stream.getReader = () => ({ read: async () => ({ done: false, value: "not bytes" }), cancel: async () => { odd.state.cancelled = true; } });
  assert.equal((await worker.fetch(request(HTTP_PATH, { headers: echoHeaders, body: odd.stream }), openEnv(unused))).status, 502);
  assert.ok(odd.state.cancelled);
  assert.equal(rt.timers.size, 0);
});

function upgraded(protocol = SUBPROTOCOL) {
  const upstream = new FakeSocket();
  return { upstream, respond: () => ({ status: 101, headers: new Headers({ "Sec-WebSocket-Protocol": protocol }), webSocket: upstream, body: null }) };
}

test("the WebSocket proxy forwards one fixed upgrade and preserves text, binary and subprotocol", async () => {
  const rt = fakeRuntime();
  const { upstream, respond } = upgraded();
  const binding = fakeBinding(respond);
  const r = await createProbeWorker(rt).fetch(request(WS_PATH, { method: "GET", headers: wsHeaders }), openEnv(binding));
  assert.equal(r.status, 101);
  assert.equal(r.headers.get("Sec-WebSocket-Protocol"), SUBPROTOCOL);
  const [client, server] = rt.pairs[0];
  assert.equal(r.webSocket, client);
  const [{ url, init }] = binding.calls;
  assert.equal(url, UPSTREAM_ORIGIN + WS_PATH);
  assert.deepEqual(init.headers, { Upgrade: "websocket", [ASSERTION_HEADER]: TOKEN, "Sec-WebSocket-Protocol": SUBPROTOCOL });
  assert.ok(server.accepted && upstream.accepted);

  const binary = new Uint8Array([0, 1, 255]).buffer;
  server.emit("message", { data: "synthetic-text" });
  server.emit("message", { data: binary });
  upstream.emit("message", { data: "synthetic-text" });
  upstream.emit("message", { data: binary });
  assert.deepEqual(upstream.sent, ["synthetic-text", binary]);
  assert.deepEqual(server.sent, ["synthetic-text", binary]);
  assert.equal(typeof upstream.sent[0], "string");
  assert.ok(server.sent[1] instanceof ArrayBuffer);
  assert.equal(rt.timers.size, 1);
  assert.equal([...rt.timers.values()][0].ms, LIMITS.lifetimeMs);

  upstream.emit("close", { code: 1000, reason: "probe frame budget reached" });
  assert.deepEqual(server.closed, { code: 1000, reason: "probe frame budget reached" });
  assert.equal(rt.timers.size, 0);
  server.emit("message", { data: "late" });
  assert.equal(upstream.sent.length, 2);
});

test("the WebSocket upgrade needs the exact subprotocol and a 101 with it from upstream", async () => {
  const rt = fakeRuntime();
  const worker = createProbeWorker(rt);
  const unused = fakeBinding(upgraded().respond);
  for (const protocol of [undefined, "tether-agent.v1", `${SUBPROTOCOL}, other`]) {
    const headers = { ...wsHeaders };
    if (protocol === undefined) delete headers["Sec-WebSocket-Protocol"]; else headers["Sec-WebSocket-Protocol"] = protocol;
    assert.equal((await worker.fetch(request(WS_PATH, { method: "GET", headers }), openEnv(unused))).status, 400);
  }
  assert.equal(unused.calls.length, 0);

  const refused = fakeBinding(() => new Response("the authenticated subject is not allowed for this probe\n", { status: 403 }));
  assert.equal((await worker.fetch(request(WS_PATH, { method: "GET", headers: wsHeaders }), openEnv(refused))).status, 403);
  const odd = fakeBinding(() => new Response("teapot", { status: 418 }));
  assert.equal((await worker.fetch(request(WS_PATH, { method: "GET", headers: wsHeaders }), openEnv(odd))).status, 502);
  const mismatch = upgraded("other");
  assert.equal((await worker.fetch(request(WS_PATH, { method: "GET", headers: wsHeaders }), openEnv(fakeBinding(mismatch.respond)))).status, 502);
  assert.equal(mismatch.upstream.closed.code, 1002);
  assert.equal(rt.pairs.length, 0);
  assert.equal(rt.timers.size, 0);
});

function relayed(limits = LIMITS) {
  const rt = fakeRuntime();
  const client = new FakeSocket();
  const upstream = new FakeSocket();
  relay(client, upstream, rt, limits);
  return { rt, client, upstream };
}

test("the relay lifetime is one non-extendible timer that closes both ends", () => {
  const { rt, client, upstream } = relayed();
  client.emit("message", { data: "busy" });
  assert.equal(rt.timers.size, 1);
  rt.fireAll();
  assert.deepEqual(client.closed, { code: 4000, reason: "probe lifetime ended" });
  assert.deepEqual(upstream.closed, { code: 4000, reason: "probe lifetime ended" });
});

test("the relay closes on an oversized message, the frame budget or an error", () => {
  let r = relayed();
  r.client.emit("message", { data: "é".repeat(LIMITS.message / 2 + 1) });
  assert.equal(r.upstream.sent.length, 0);
  assert.equal(r.upstream.closed.code, 1009);
  assert.equal(r.rt.timers.size, 0);

  r = relayed({ ...LIMITS, frames: 2 });
  for (const data of ["1", "2", "3"]) r.upstream.emit("message", { data });
  assert.deepEqual(r.client.sent, ["1", "2"]);
  assert.equal(r.client.closed.code, 1008);

  r = relayed();
  r.client.emit("message", { data: new Blob(["x"]) });
  assert.equal(r.upstream.closed.code, 1009);

  r = relayed();
  r.upstream.emit("error", {});
  assert.equal(r.client.closed.code, 1011);
  r.client.emit("close", { code: 1000, reason: "" });
  assert.equal(r.rt.timers.size, 0);
});

test("close codes stay sendable", () => {
  assert.deepEqual([1000, 1001, 1008, 1009, 1011, 3000, 4999].map(closeCode), [1000, 1001, 1008, 1009, 1011, 3000, 4999]);
  assert.deepEqual([1005, 1006, 1015, 999, 5000, undefined].map(closeCode), [1000, 1011, 1011, 1011, 1011, 1011]);
  const { client, upstream } = relayed();
  upstream.emit("close", { code: 1006, reason: "x".repeat(200) });
  assert.deepEqual(client.closed, { code: 1011, reason: "" });
});

test("the sample Wrangler config stays closed and non-deployable", () => {
  const sample = readFileSync(new URL("./wrangler.sample.jsonc", import.meta.url), "utf8");
  assert.match(sample, /"name":\s*"tether-dev"/);
  assert.match(sample, /"workers_dev":\s*false/);
  assert.match(sample, /"TETHER_PROBE_GATE":\s*"closed"/);
  assert.match(sample, /"service_id":\s*""/);
  assert.doesNotMatch(sample, /compatibility_date"\s*:/);
  assert.ok(!sample.includes(GATE_VALUE));
});

test("relay explicitly selects ArrayBuffer before accepting both sockets", () => {
 const {client, upstream} = relayed();
 assert.equal(client.binaryType, "arraybuffer");
 assert.equal(upstream.binaryType, "arraybuffer");
 assert.equal(client.acceptedBinaryType, "arraybuffer");
 assert.equal(upstream.acceptedBinaryType, "arraybuffer");
 const bytes = new Uint8Array([0,255,1]).buffer;
 client.emit("message", {data: client.binaryType === "arraybuffer" ? bytes : new Blob([bytes])});
 upstream.emit("message", {data: upstream.binaryType === "arraybuffer" ? bytes : new Blob([bytes])});
 assert.deepEqual(upstream.sent, [bytes]); assert.deepEqual(client.sent, [bytes]);
 assert.equal(client.closed, null); assert.equal(upstream.closed, null);
});
