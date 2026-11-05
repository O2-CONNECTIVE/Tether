import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installCapture } from './capture-capability.mjs';

function fixture(capabilities, options = {}) {
  const value = Object.hasOwn(options, 'value') ? options.value : { value: { sessionId: 'PRIVATE_SESSION', capabilities } };
  const saved = [];
  const shapes = [];
  let jsonCalls = 0;
  class FakeResponse { async json() { jsonCalls += 1; if (options.jsonError) throw options.jsonError; return value; } }
  const response = new FakeResponse();
  const calls = [];
  const writes = [];
  const target = { fetch: async (...args) => { calls.push(args); if (options.error) throw options.error; return response; } };
  const restore = installCapture({ target, responseType: FakeResponse, profile: 'C:\\owned\\profile', output: 'output',
    filesystem: { existsSync: () => options.preexisting === true, writeFileSync: (file, text) => {
      const name = file.endsWith('profile-capability-shape.json') ? 'shape' : 'legacy';
      writes.push(name);
      if (options.writeError) throw new Error('PRIVATE_WRITE_ERROR');
      if (options.failFirst === name && writes.filter(x => x === name).length === 1) throw new Error('PRIVATE_WRITE_ERROR');
      (file.endsWith('profile-capability-shape.json') ? shapes : saved).push(JSON.parse(text));
    } } });
  const request = { method: 'POST', body: JSON.stringify({ capabilities: { alwaysMatch: {
    'tauri:options': { application: 'PRIVATE_EXE', webviewOptions: { userDataFolder: 'C:\\owned\\profile' } }
  } } }), signal: new AbortController().signal };
  return { value, response, saved, shapes, calls, writes, jsonCalls: () => jsonCalls, target, restore, request };
}

test('request args/signal, response and parsed value remain identical; only safe booleans are recorded', async () => {
  const x = fixture({ msedge: { userDataDir: 'C:\\owned\\profile', msedgedriverVersion: 'PRIVATE_DRIVER_DETAIL' } });
  try {
    const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
    assert.equal(response, x.response);
    assert.equal(x.calls[0][1], x.request);
    assert.equal(await response.json(), x.value);
    assert.equal(x.jsonCalls(), 1);
    assert.equal(x.calls.length, 1);
    assert.deepEqual(x.writes, ['legacy', 'shape', 'legacy', 'shape']);
    assert.deepEqual(x.saved.at(-1), { schema: 1, requests: 1, settlements: 1,
      requestedMatchesEnvironment: true, requestedAbsentBeforeRequest: true,
      capabilityObserved: true, capabilityMatchesRequested: true, capabilityMapping: 'requested-root', captureFailed: false });
    assert.ok(!JSON.stringify(x.saved).includes('PRIVATE_'));
    assert.ok(!JSON.stringify(x.saved).includes('owned'));
  } finally { x.restore(); }
});

test('synthetic envelope/type cases expose only fixed JSON kinds and known locations', async () => {
  for (const [value, code, key, kind] of [
    [null, 'envelope-not-object', 'envelopeShape', 'null'],
    [[], 'envelope-not-object', 'envelopeShape', 'array'],
    [{}, 'value-not-object', 'valueShape', 'missing'],
    [{ value: 3 }, 'value-not-object', 'valueShape', 'number'],
    [{ value: {} }, 'capabilities-not-object', 'capabilitiesShape', 'missing'],
    [{ value: { capabilities: 'PRIVATE_VALUE' } }, 'capabilities-not-object', 'capabilitiesShape', 'string'],
    [{ value: { capabilities: { msedge: [] } } }, 'candidate-missing', 'nestedContainerShape', 'array'],
  ]) {
    const x = fixture({}, { value });
    try {
      const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
      assert.equal(await response.json(), value);
      assert.equal(x.shapes.at(-1).candidateClass, code);
      assert.equal(x.shapes.at(-1)[key], kind);
      assert.ok(!JSON.stringify(x.shapes).includes('PRIVATE_'));
      assert.equal(x.calls.length, 1); assert.equal(x.jsonCalls(), 1);
    } finally { x.restore(); }
  }
});

test('synthetic candidate JSON kinds and flat/nested layouts remain distinct without payloads', async () => {
  for (const [value, kind] of [[null, 'null'], [false, 'boolean'], [19, 'number'], [[], 'array'], [{}, 'object']]) {
    const x = fixture({ 'msedge.userDataDir': value, PRIVATE_UNKNOWN_KEY: 'PRIVATE_SECRET' });
    try {
      const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request); await response.json();
      assert.equal(x.shapes.at(-1).candidateClass, 'candidate-not-string');
      assert.equal(x.shapes.at(-1).flatShape, kind);
      assert.ok(!JSON.stringify(x.shapes).includes('PRIVATE_'));
    } finally { x.restore(); }
  }
  for (const [caps, code] of [[{ 'msedge.userDataDir': 'C:\\owned\\profile' }, 'candidate-valid-flat'],
    [{ msedge: { userDataDir: 'C:\\owned\\profile' } }, 'candidate-valid-nested'],
    [{ 'msedge.userDataDir': 'C:\\owned\\profile\\..\\profile' }, 'candidate-invalid-path']]) {
    const x = fixture(caps);
    try {
      const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request); await response.json();
      assert.equal(x.shapes.at(-1).candidateClass, code);
      assert.ok(!JSON.stringify(x.shapes).includes('owned'));
    } finally { x.restore(); }
  }
});

test('synthetic later successful writes retain bounded capture-failure site evidence', async () => {
  for (const name of ['legacy', 'shape']) {
    const x = fixture({ msedge: { userDataDir: 'C:\\owned\\profile' } }, { failFirst: name });
    try {
      const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
      assert.equal(await response.json(), x.value);
      assert.deepEqual(x.shapes.at(-1).failureSites, [name === 'legacy' ? 'legacy-record-write' : 'shape-record-write']);
      assert.equal(x.saved.at(-1).captureFailed, name === 'legacy');
      assert.deepEqual(x.writes, ['legacy', 'shape', 'legacy', 'shape']);
      assert.equal(x.calls.length, 1); assert.equal(x.jsonCalls(), 1);
    } finally { x.restore(); }
  }
});

test('synthetic original JSON rejection preserves the exact error object and performs no extra read', async () => {
  const error = new SyntaxError('PRIVATE_JSON_EXCEPTION');
  const x = fixture({}, { jsonError: error });
  try {
    const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
    await assert.rejects(response.json(), actual => actual === error);
    assert.equal(x.calls.length, 1); assert.equal(x.jsonCalls(), 1);
    assert.equal(x.shapes.at(-1).candidateClass, 'not-observed');
    assert.ok(!JSON.stringify(x.shapes).includes('PRIVATE_'));
  } finally { x.restore(); }
});

test('synthetic missing/duplicate/non-string capability cases have distinct bounded shape evidence', async () => {
  for (const [caps, expected] of [[{}, 'candidate-missing'],
    [{ 'msedge.userDataDir': null, msedge: { userDataDir: 'C:\\owned\\profile' } }, 'candidate-duplicated'],
    [{ 'msedge.userDataDir': 27 }, 'candidate-not-string']]) {
    const x = fixture(caps);
    try {
      const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
      assert.equal(await response.json(), x.value);
      assert.equal(x.shapes.at(-1)?.candidateClass, expected);
      assert.equal(x.saved.at(-1).capabilityObserved, false);
      assert.equal(x.calls.length, 1);
    } finally { x.restore(); }
  }
});

test('only the explicit EBWebView child is classified separately; arbitrary descendants stay outside', async () => {
  for (const [value, expected] of [['C:\\owned\\profile\\EBWebView', 'requested-child-ebwebview'],
    ['C:\\owned\\profile\\anything', 'outside-requested-profile'],
    ['C:\\owned\\profilesuffix', 'outside-requested-profile']]) {
    const x = fixture({ msedge: { userDataDir: value } });
    try {
      const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
      await response.json();
      assert.equal(x.saved.at(-1).capabilityMapping, expected);
    } finally { x.restore(); }
  }
});

test('absent/different/ambiguous returned capability remains false, while response is unchanged', async () => {
  for (const capabilities of [{}, { msedge: { userDataDir: 'C:\\default\\profile' } },
    { msedge: { userDataDir: 'C:\\owned\\profile' }, 'msedge.userDataDir': 'C:\\owned\\profile' },
    { msedge: { userDataDir: 'C:\\owned\\profile' }, 'msedge.userDataDir': null },
    { msedge: { userDataDir: 'C:\\owned\\profile\\..\\profile' } }]) {
    const x = fixture(capabilities);
    try {
      const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
      assert.equal(await response.json(), x.value);
      assert.equal(x.saved.at(-1).capabilityMatchesRequested, false);
    } finally { x.restore(); }
  }
});

test('preexisting requested profile fails evidence without changing request or result', async () => {
  const x = fixture({ 'msedge.userDataDir': 'C:\\owned\\profile' }, { preexisting: true });
  try {
    const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
    assert.equal(await response.json(), x.value);
    assert.equal(x.saved.at(-1).requestedAbsentBeforeRequest, false);
  } finally { x.restore(); }
});

test('original timeout object propagates and capture write failure never changes native outcome', async () => {
  const error = new Error('NATIVE_DRIVER_TIMEOUT');
  let x = fixture({}, { error });
  try { await assert.rejects(x.target.fetch('http://127.0.0.1:4444/session', x.request), actual => actual === error); }
  finally { x.restore(); }
  x = fixture({ msedge: { userDataDir: 'C:\\owned\\profile' } }, { writeError: true });
  try {
    const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
    assert.equal(await response.json(), x.value);
    assert.equal(x.saved.length, 0);
  } finally { x.restore(); }
});


test('synthetic rejected path records the first validator predicate without changing native objects', async () => {
  for (const [candidate, predicate] of [['relative?', 'drive-prefix'], ['C:\\bad?:colon', 'forbidden-character'],
    ['C:\\bad:colon', 'extra-colon'], ['C:\\ok\\', 'empty-component'],
    ['C:\\..', 'dot-component'], ['C:\\name.', 'trailing-dot-or-space']]) {
    const x = fixture({ msedge: { userDataDir: candidate } });
    try {
      const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
      assert.equal(response, x.response); assert.equal(await response.json(), x.value);
      assert.equal(x.shapes.at(-1).schema, 2);
      assert.equal(x.shapes.at(-1).pathPredicate, predicate);
      assert.equal(x.shapes.at(-1).candidateClass, 'candidate-invalid-path');
      assert.equal(x.saved.at(-1).capabilityObserved, false);
      assert.equal(x.saved.at(-1).capabilityMatchesRequested, false);
      assert.equal(x.saved.at(-1).captureFailed, false);
      assert.equal(x.calls.length, 1); assert.equal(x.jsonCalls(), 1);
      assert.equal(x.calls[0][1], x.request); assert.equal(x.calls[0][1].signal, x.request.signal);
      assert.ok(!JSON.stringify(x.shapes).includes(candidate));
    } finally { x.restore(); }
  }
});
