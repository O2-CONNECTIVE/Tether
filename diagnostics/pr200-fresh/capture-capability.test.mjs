import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installCapture } from './capture-capability.mjs';

function fixture(capabilities, options = {}) {
  const value = { value: { sessionId: 'PRIVATE_SESSION', capabilities } };
  const saved = [];
  class FakeResponse { async json() { return value; } }
  const response = new FakeResponse();
  const calls = [];
  const target = { fetch: async (...args) => { calls.push(args); if (options.error) throw options.error; return response; } };
  const restore = installCapture({ target, responseType: FakeResponse, profile: 'C:\\owned\\profile', output: 'output',
    filesystem: { existsSync: () => options.preexisting === true, writeFileSync: (file, text) => {
      if (options.writeError) throw new Error('PRIVATE_WRITE_ERROR');
      saved.push(JSON.parse(text));
    } } });
  const request = { method: 'POST', body: JSON.stringify({ capabilities: { alwaysMatch: {
    'tauri:options': { application: 'PRIVATE_EXE', webviewOptions: { userDataFolder: 'C:\\owned\\profile' } }
  } } }), signal: new AbortController().signal };
  return { value, response, saved, calls, target, restore, request };
}

test('request args/signal, response and parsed value remain identical; only safe booleans are recorded', async () => {
  const x = fixture({ msedge: { userDataDir: 'C:\\owned\\profile', msedgedriverVersion: 'PRIVATE_DRIVER_DETAIL' } });
  try {
    const response = await x.target.fetch('http://127.0.0.1:4444/session', x.request);
    assert.equal(response, x.response);
    assert.equal(x.calls[0][1], x.request);
    assert.equal(await response.json(), x.value);
    assert.deepEqual(x.saved.at(-1), { schema: 1, requests: 1, settlements: 1,
      requestedMatchesEnvironment: true, requestedAbsentBeforeRequest: true,
      capabilityObserved: true, capabilityMatchesRequested: true, capabilityMapping: 'requested-root', captureFailed: false });
    assert.ok(!JSON.stringify(x.saved).includes('PRIVATE_'));
    assert.ok(!JSON.stringify(x.saved).includes('owned'));
  } finally { x.restore(); }
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
