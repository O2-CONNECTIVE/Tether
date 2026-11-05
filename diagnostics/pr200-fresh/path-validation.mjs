import path from 'node:path';

// Same ordered predicates and normalized result as PR211. Codes describe this
// diagnostic validator, never OS path validity. Values never leave this function.
export function normalizeProfilePath(value, rejected = () => {}) {
  const no = code => { rejected(code); return null; };
  if (typeof value !== 'string') return no('non-string');
  if (!/^[A-Za-z]:[\\/].+/.test(value)) return no('drive-prefix');
  if (/[\x00\r\n"*?<>|]/.test(value)) return no('forbidden-character');
  if (value.slice(2).includes(':')) return no('extra-colon');
  const normalized = value.replaceAll('/', '\\');
  for (const part of normalized.slice(3).split('\\')) {
    if (!part) return no('empty-component');
    if (part === '.' || part === '..') return no('dot-component');
    if (/[ .]$/.test(part)) return no('trailing-dot-or-space');
  }
  return path.win32.normalize(normalized).toLowerCase();
}
