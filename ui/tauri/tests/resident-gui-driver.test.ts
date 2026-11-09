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
  const buttons = {
    '#resident-on': { disabled: false, click: () => click('on') },
    '#resident-off': { disabled: true, click: () => click('off') },
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
    buttons['#resident-on'].disabled = intent === 'on';
    buttons['#resident-off'].disabled = intent === 'off';
    if (intent === 'off' && mode === 'wrong-transition') dataset.residentTransitionId = String(sequence - 1);
    if (intent === 'off' && mode === 'rejected-off') { dataset.residentReceipt = 'rejected'; dataset.residentTransitionOutcome = 'rejected'; }
    if (intent === 'off' && mode === 'unaccepted') dataset.residentReceipt = 'unavailable';
  }
  await runInNewContext(driver, {
    performance: { now: () => now },
    setTimeout: (callback: () => void, delay: number) => { now += delay; queueMicrotask(callback); },
    document: { querySelector: (selector: string) => selector === '#resident-panel' ? { dataset }
      : buttons[selector as keyof typeof buttons] },
    window: { __TAURI__: { event: { emit: (_: string, event: string) => {
      events.push(event);
      if (event === 'on-applied' && mode === 'spontaneous-off') {
        buttons['#resident-on'].disabled = false; buttons['#resident-off'].disabled = true;
        dataset.residentStandby = 'off'; dataset.residentRegistration = 'disconnected';
      }
      return Promise.resolve();
    } } } },
  });
  return { events, onClicks, offClicks };
}
void test('GUI driver requires its own ON and OFF accepted/applied identities, not predicted counters', async () => {
  assert.deepEqual(await run('normal'), { events: ['initializer-started', 'on-applied', 'off-applied', 'passed'], onClicks: 1, offClicks: 1 });
  assert.deepEqual(await run('stale-once'), { events: ['initializer-started', 'on-applied', 'off-applied', 'passed'], onClicks: 2, offClicks: 1 });
});
void test('GUI driver rejects target-enabled without OFF and mismatched or missing receipt proof', async () => {
  for (const mode of ['spontaneous-off', 'wrong-transition', 'unaccepted', 'rejected-off']) {
    const result = await run(mode);
    assert.match(result.events.at(-1) ?? '', /^failed:off:/, mode);
    assert.equal(result.events.includes('passed'), false, mode);
    assert.equal(result.offClicks, mode === 'spontaneous-off' ? 0 : 1, mode);
  }
});

async function classify(mode: string) {
  let now = 0, clicks = 0;
  const events: string[] = [];
  const dataset: Record<string, string> = { residentRequestId: '0', residentRevision: mode === 'identity' ? '0' : '1',
    residentReceipt: 'none', residentStandby: 'off', residentRegistration: 'disconnected',
    residentTransitionId: '0', residentTransitionIntent: 'none', residentTransitionOutcome: 'none' };
  const button = { disabled: false, click() {
    clicks++;
    if (mode === 'unexpected') throw new Error('https://secret.invalid/private-value');
    if (mode === 'phase-timeout') { button.disabled = true; return; }
    dataset.residentRequestId = String(clicks); dataset.residentRevision = String(clicks + 1);
    dataset.residentReceipt = mode === 'receipt' ? 'unavailable' : mode === 'retry-budget' ? 'stale' : 'accepted';
    dataset.residentTransitionId = String(clicks); dataset.residentTransitionIntent = 'on';
    dataset.residentTransitionOutcome = mode === 'transition' ? 'unknown' : 'applied';
    dataset.residentStandby = 'on';
    button.disabled = mode !== 'retry-budget';
  } };
  await runInNewContext(driver, {
    performance: { now: () => now }, setTimeout: (f: () => void, ms: number) => { now += ms; queueMicrotask(f); },
    document: { querySelector: (s: string) => s === '#resident-panel' ? { dataset }
      : s === '#resident-on' ? button : { disabled: mode !== 'auth-timeout' } },
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
