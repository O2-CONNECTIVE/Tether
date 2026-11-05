// Synthetic predicate/equivalence fixtures; none reconstruct PR211's discarded value.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { normalizeProfilePath } from './path-validation.mjs';

// Verbatim normalization body from PR211 bec16ca capture-capability.mjs.
function before(value) {
  if (typeof value !== 'string' || !/^[A-Za-z]:[\\/].+/.test(value) || /[\x00\r\n"*?<>|]/.test(value) || value.slice(2).includes(':')) return null;
  const normalized = value.replaceAll('/', '\\');
  if (normalized.slice(3).split('\\').some(part => !part || part === '.' || part === '..' || /[ .]$/.test(part))) return null;
  return path.win32.normalize(normalized).toLowerCase();
}

test('synthetic first rejected predicate follows original short-circuit order', () => {
  for (const [value, expected] of [
    [null, 'non-string'], [17, 'non-string'], ['', 'drive-prefix'], ['C:\\', 'drive-prefix'],
    ['relative?', 'drive-prefix'], ['C:\\bad?:colon', 'forbidden-character'],
    ['C:\\bad:colon\\..', 'extra-colon'], ['C:\\ok\\', 'empty-component'],
    ['C:\\..\\bad ', 'dot-component'], ['C:\\bad \\..', 'trailing-dot-or-space'],
    ['C:/ok/path', 'accepted'], ['c:\\ok\\EBWebView', 'accepted'],
  ]) {
    const codes = [];
    const actual = normalizeProfilePath(value, code => codes.push(code));
    assert.equal(actual, before(value));
    assert.deepEqual(codes, expected === 'accepted' ? [] : [expected]);
  }
});

test('synthetic deterministic corpus preserves exact normalization and acceptance', () => {
  const atoms = ['', 'x', '.', '..', 'x ', 'x.', 'x:y', 'x?', '\n', '日本語', 'EBWebView'];
  let count = 0;
  for (const prefix of ['C:\\', 'C:/', 'c:\\', '\\\\host\\', 'relative/', 'C:']) {
    for (const separator of ['\\', '/']) for (const a of atoms) for (const b of atoms) for (const c of atoms) {
      const input = prefix + [a, b, c].join(separator);
      assert.equal(normalizeProfilePath(input), before(input));
      count += 1;
    }
  }
  assert.equal(count, 15972);
  const poison = { toString() { throw new Error('must not coerce'); } };
  assert.equal(normalizeProfilePath(poison), null);
});
