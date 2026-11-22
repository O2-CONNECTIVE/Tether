import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decodeReceipt, decodeStatus } from '../src/resident/model.ts';
const sample = (change: Record<string, unknown> = {}) => ({ protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision: 1,
  validForMs: 3000, standby: 'on', registration: 'authenticated', caller: null, work: 'idle', cleanup: 'confirmed', canRequestOn: false, canRequestOff: true,
  transition: null, standbyUntil: 1_900_000_000_000, standbyEndingSoon: false, standbyExpired: false, ...change });

// The host GUI shows "until hh:mm", warns ten minutes before it, and says when
// standby stopped because it ran out (#451). Go owns that end; the GUI only
// carries what Go said, so the three keys travel exactly as Go wrote them.
void test('status carries when standby ends, as Go wrote it', () => {
  assert.deepEqual(decodeStatus(sample({ standbyEndingSoon: true })), { source: 'resident-owner', observation: { revision: 1, validForMs: 3000,
    standby: 'on', registration: 'authenticated', caller: null, work: 'idle', cleanup: 'confirmed', canRequestOn: false, canRequestOff: true,
    transition: null, standbyUntil: 1_900_000_000_000, standbyEndingSoon: true, standbyExpired: false } });
  const expired = decodeStatus(sample({ standby: 'off', canRequestOn: true, canRequestOff: false, standbyUntil: null, standbyExpired: true }));
  assert.equal(expired?.source === 'resident-owner' && expired.observation?.standbyExpired, true);
  assert.ok(decodeStatus(sample({ standbyUntil: Number.MAX_SAFE_INTEGER })));
});

// A status without the end, or with one no clock can show, is not Go's word:
// the GUI must not invent a deadline or drop the warning.
void test('status without a valid standby end is refused', () => {
  const { standbyUntil: _u, standbyEndingSoon: _s, standbyExpired: _e, ...withoutEnd } = sample();
  void _u; void _s; void _e;
  assert.equal(decodeStatus(withoutEnd), undefined);
  for (const change of [{ standbyUntil: 0 }, { standbyUntil: -1 }, { standbyUntil: 1.5 }, { standbyUntil: Number.MAX_SAFE_INTEGER + 1 },
    { standbyUntil: '1900000000000' }, { standbyUntil: null, standbyEndingSoon: true }, { standbyEndingSoon: null },
    { standbyExpired: 'false' }, { standbyExpired: undefined }]) {
    assert.equal(decodeStatus(sample(change)), undefined, JSON.stringify(change));
  }
});

// Extending names the request it answers, like ON and OFF.
void test('extend receipt is matched to its own request', () => {
  const receipt = { protocolVersion: 1, requestId: 7, intent: 'extend', disposition: 'accepted' };
  assert.equal(decodeReceipt(receipt, 7, 'extend'), 'accepted');
  assert.equal(decodeReceipt(receipt, 7, 'on'), undefined);
});
