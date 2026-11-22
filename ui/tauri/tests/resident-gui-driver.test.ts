import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
const driver = readFileSync(new URL('../src-tauri/tests/resident-gui/drive.js', import.meta.url), 'utf8');
async function run(mode: string) {
  let now = 0, onClicks = 0, offClicks = 0, sequence = 40;
  const events: string[] = [];
  const dataset: Record<string, string> = { residentRequestId: '0', residentRevision: '1', residentReceipt: 'none',
    residentStandby: 'off', residentRegistration: 'disconnected', residentTransitionId: '0',
    residentTransitionIntent: 'none', residentTransitionOutcome: 'none' };
  const standbySwitch = {
    disabled: false,
    getAttribute: (name: string) => name === 'aria-checked' ? String(dataset.residentStandby === 'on') : null,
    click: () => click(dataset.residentStandby === 'on' ? 'off' : 'on'),
  };
  function click(intent: 'on' | 'off') {
    if (intent === 'on') onClicks++; else offClicks++;
    dataset.residentRequestId = String(++sequence);
    dataset.residentRevision = String(Number(dataset.residentRevision) + 1);
    dataset.residentReceipt = 'accepted';
    dataset.residentStandby = intent;
    dataset.residentRegistration = intent === 'on' ? 'authenticated' : 'disconnected';
    dataset.residentTransitionId = String(sequence);
    dataset.residentTransitionIntent = intent;
    dataset.residentTransitionOutcome = 'applied';
    if (mode === 'stale-once' && onClicks === 1 && intent === 'on') {
      dataset.residentReceipt = 'stale'; dataset.residentStandby = 'off';
      dataset.residentRegistration = 'disconnected'; dataset.residentTransitionOutcome = 'rejected'; return;
    }
    if (intent === 'off' && mode === 'wrong-transition') dataset.residentTransitionId = String(sequence - 1);
    if (intent === 'off' && mode === 'rejected-off') { dataset.residentReceipt = 'rejected'; dataset.residentTransitionOutcome = 'rejected'; }
    if (intent === 'off' && mode === 'unaccepted') dataset.residentReceipt = 'unavailable';
  }
  const document = Object.assign(new EventTarget(), {
    hidden: false, visibilityState: 'visible',
    querySelector: (selector: string) => selector === '#resident-panel' ? { dataset }
      : selector === '#resident-switch' ? standbySwitch : null,
  });
  const window = Object.assign(new EventTarget(), {
    __TAURI__: { event: { emit: (_: string, event: string) => {
      events.push(event);
      if (event === 'on-applied' && mode !== 'no-hidden-event') {
        document.hidden = true; document.visibilityState = 'hidden';
        document.dispatchEvent(new Event('visibilitychange'));
      }
      if (event === 'window-hidden' && mode !== 'no-reopen-event') {
        document.hidden = false; document.visibilityState = 'visible';
        document.dispatchEvent(new Event('visibilitychange'));
        window.dispatchEvent(new Event('tether-fixture-window-reopened'));
        if (mode === 'spontaneous-off') {
          dataset.residentStandby = 'off'; dataset.residentRegistration = 'disconnected';
        }
        if (mode === 'wrong-reopen-id') dataset.residentRequestId = String(sequence + 1);
        if (mode === 'no-reopen-observation') dataset.residentRevision = '0';
      }
      return Promise.resolve();
    } } },
  });
  await runInNewContext(driver, {
    performance: { now: () => now },
    setTimeout: (callback: () => void, delay: number) => { now += delay; queueMicrotask(callback); },
    document, window,
  });
  return { events, onClicks, offClicks };
}
void test('GUI driver requires its own ON and OFF accepted/applied identities, not predicted counters', async () => {
  const events = ['initializer-started', 'on-applied', 'window-hidden', 'off-applied', 'passed'];
  assert.deepEqual(await run('normal'), { events, onClicks: 1, offClicks: 1 });
  assert.deepEqual(await run('stale-once'), { events, onClicks: 2, offClicks: 1 });
});
void test('GUI driver rejects target-enabled without OFF and mismatched or missing receipt proof', async () => {
  for (const mode of ['wrong-transition', 'unaccepted', 'rejected-off']) {
    const result = await run(mode);
    assert.match(result.events.at(-1) ?? '', /^failed:off:/, mode);
    assert.equal(result.events.includes('passed'), false, mode);
    assert.equal(result.offClicks, 1, mode);
  }
});

void test('GUI driver requires the hidden event, native reopen and matching ON before it clicks OFF', async () => {
  for (const mode of ['no-hidden-event', 'no-reopen-event', 'wrong-reopen-id', 'no-reopen-observation', 'spontaneous-off']) {
    const result = await run(mode);
    const hidden = mode === 'no-hidden-event' ? [] : ['window-hidden'];
    assert.deepEqual(result.events, ['initializer-started', 'on-applied', ...hidden, 'failed:reopen:phase-timeout'], mode);
    assert.equal(result.offClicks, 0, mode);
  }
});

async function classify(mode: string) {
  let now = 0, clicks = 0;
  const events: string[] = [];
  const dataset: Record<string, string> = { residentRequestId: '0', residentRevision: mode === 'identity' ? '0' : '1',
    residentReceipt: 'none', residentStandby: 'off', residentRegistration: 'disconnected',
    residentTransitionId: '0', residentTransitionIntent: 'none', residentTransitionOutcome: 'none' };
  const button = { disabled: false,
    getAttribute: (name: string) => name === 'aria-checked' ? String(dataset.residentStandby === 'on') : null,
    click() {
    clicks++;
    if (mode === 'unexpected') throw new Error('https://secret.invalid/private-value');
    if (mode === 'phase-timeout') { button.disabled = true; return; }
    dataset.residentRequestId = String(clicks); dataset.residentRevision = String(clicks + 1);
    dataset.residentReceipt = mode === 'receipt' ? 'unavailable' : mode === 'retry-budget' ? 'stale' : 'accepted';
    dataset.residentTransitionId = String(clicks); dataset.residentTransitionIntent = 'on';
    dataset.residentTransitionOutcome = mode === 'transition' ? 'unknown' : mode === 'retry-budget' ? 'rejected' : 'applied';
    if (mode !== 'retry-budget') dataset.residentStandby = 'on';
  } };
  await runInNewContext(driver, {
    performance: { now: () => now }, setTimeout: (f: () => void, ms: number) => { now += ms; queueMicrotask(f); },
    document: { querySelector: (s: string) => s === '#resident-panel' ? { dataset }
      : s === '#resident-switch' ? button : null },
    window: { __TAURI__: { event: { emit: (_: string, v: string) => { events.push(v); return Promise.resolve(); } } } },
  });
  return { events, clicks };
}
void test('GUI driver reports only fixed failure phase and reason without changing the failed verdict', async () => {
  for (const reason of ['identity', 'receipt', 'transition', 'retry-budget', 'phase-timeout', 'unexpected', 'auth-timeout']) {
    const r = await classify(reason);
    assert.equal(r.events.at(-1), `failed:${reason === 'auth-timeout' ? 'auth' : 'on'}:${reason}`);
    assert.equal(r.events.includes('passed'), false);
    assert.equal(JSON.stringify(r).includes('secret.invalid'), false);
    assert.equal(r.clicks, reason === 'identity' ? 0 : reason === 'retry-budget' ? 8 : 1);
  }
});
void test('missing initial bridge reports its fixed timeout only if the bridge later becomes available', async () => {
  let now = 0; const events: string[] = [];
  await runInNewContext(driver, {
    performance: { now: () => now },
    setTimeout: (f: () => void, ms: number) => { now += ms; queueMicrotask(f); },
    window: { get __TAURI__() {
      return now >= 20000 ? { event: { emit: (_: string, v: string) => { events.push(v); return Promise.resolve(); } } } : undefined;
    } },
  });
  assert.deepEqual(events, ['failed:init:bridge-timeout']);
  assert.equal(now, 20000);
});
