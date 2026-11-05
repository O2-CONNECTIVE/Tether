import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createShellController, type ShellState } from '../src/controller.ts';
import { presentWindowsObservation } from '../src/host-status/windows-observation.ts';

const observation = (state: unknown, feed = 'fresh') => ({ scope: 'windows-shell-observation', version: 1, feed, state });
async function statesFrom(payload: unknown): Promise<ShellState[]> {
  const states: ShellState[] = [];
  const controller = createShellController({ invoke: () => Promise.resolve(payload), onChange: state => states.push(state) });
  await controller.refresh(); controller.dispose(); return states;
}
function assertNoAuthority(result: ReturnType<typeof presentWindowsObservation>) {
  assert.equal(result.scope, 'windows-host-presentation');
  assert.equal(result.version, 1);
  assert.equal(result.authority, 'not_established');
  assert.equal(result.canStart, false); assert.equal(result.canStop, false);
  assert.equal(result.presentation.workers, '—'); assert.equal(result.presentation.remaining, '—');
  assert.notEqual(result.presentation.cleanup, 'confirmed');
}
void test('actual shell contract response passes existing controller but remains unwired', async () => {
  const contract = JSON.parse(await readFile(new URL('../contracts/shell-status-v1.json', import.meta.url), 'utf8')) as { response: unknown };
  const states = await statesFrom(contract.response);
  assert.equal(states.at(-1)?.kind, 'not_connected');
  const result = presentWindowsObservation(observation(states.at(-1)));
  assertNoAuthority(result); assert.equal(result.state, 'unwired');
  assert.equal(result.presentation.heading, 'バックエンド未接続');
});

void test('existing rejected responses, native control and Mac JSONL never become connected or authorized', async () => {
  const contract = JSON.parse(await readFile(new URL('../contracts/shell-status-v1.json', import.meta.url), 'utf8')) as { rejectedResponses: { payload: unknown }[] };
  const incompatible = [
    ...contract.rejectedResponses.map(item => item.payload),
    { protocol: 'rescue-host-control/1', kind: 'snapshot_result', generation: '1', sequence: '1', requestId: '1', identity: {}, payload: { goState: 'terminated', pending: '0', active: '0', owned: '0', goCloseConfirmed: true } },
    { version: 1, source_continuous_ms: 1, seq: 1, phase: 'closed', reason: 'deadline', grant_remaining_ms: 0, starting_workers: 0, running_workers: 0, stopping_workers: 0, cleanup: 'confirmed', terminal_cleanup_unknown: false, terminal: true },
    '{"canStart":true}', { consent: true, grant: 'PRIVATE_SENTINEL', authority: 'established' }
  ];
  for (const payload of incompatible) {
    const states = await statesFrom(payload);
    assert.equal(states.at(-1)?.kind, 'unavailable');
    const result = presentWindowsObservation(observation(states.at(-1)));
    assertNoAuthority(result); assert.equal(result.state, 'unavailable');
    assert.equal(JSON.stringify(result).includes('PRIVATE_SENTINEL'), false);
  }
});
void test('stale, EOF, invalid feed and invalid local scope/version remain unavailable', () => {
  const state = { kind: 'not_connected', diagnostic: 'SHELL_UNWIRED' };
  for (const feed of ['stale', 'eof', 'invalid', 'PRIVATE_SENTINEL']) {
    const result = presentWindowsObservation(observation(state, feed));
    assertNoAuthority(result); assert.equal(result.state, 'unavailable');
  }
  for (const input of [null, [], state, { ...observation(state), version: 2 }, { ...observation(state), scope: 'mac-status' }, { ...observation(state), authority: true }, observation({ ...state, consent: true }), observation({ kind: 'connected', diagnostic: 'SHELL_UNWIRED' })]) {
    const result = presentWindowsObservation(input);
    assertNoAuthority(result); assert.equal(result.state, 'unavailable');
  }
});
void test('real controller diagnostic preserves cleanup unknown across feed end without implying cleanup proof', async () => {
  const states: ShellState[] = [];
  // Native Tauri Err(&str) is serialized as a string rejection.
  // eslint-disable-next-line @typescript-eslint/prefer-promise-reject-errors
  const controller = createShellController({ invoke: () => Promise.reject('TERMINATION_UNCONFIRMED'), onChange: state => states.push(state) });
  await controller.refresh(); controller.dispose();
  for (const feed of ['fresh', 'stale', 'eof', 'invalid']) {
    const result = presentWindowsObservation(observation(states.at(-1), feed));
    assertNoAuthority(result); assert.equal(result.state, 'unavailable');
    assert.equal(result.presentation.heading, '回収を確認できません');
    assert.equal(result.presentation.tone, 'warning');
  }
});
void test('unknown exception and malformed frames are not exposed or erased into success by EOF', async () => {
  const states: ShellState[] = [];
  const controller = createShellController({ invoke: () => Promise.reject(new Error('PRIVATE_SENTINEL')), onChange: state => states.push(state) });
  await controller.refresh(); controller.dispose();
  for (const state of [...states, ...(await statesFrom({ wrong: 'PRIVATE_SENTINEL' }))]) {
    for (const feed of ['fresh', 'eof']) {
      const result = presentWindowsObservation(observation(state, feed));
      assertNoAuthority(result); assert.equal(result.state, 'unavailable');
      assert.equal(JSON.stringify(result).includes('PRIVATE_SENTINEL'), false);
    }
  }
});
