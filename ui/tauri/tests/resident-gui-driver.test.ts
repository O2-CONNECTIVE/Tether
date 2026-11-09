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
    assert.equal(result.events.at(-1), 'failed', mode);
    assert.equal(result.events.includes('passed'), false, mode);
    assert.equal(result.offClicks, mode === 'spontaneous-off' ? 0 : 1, mode);
  }
});
