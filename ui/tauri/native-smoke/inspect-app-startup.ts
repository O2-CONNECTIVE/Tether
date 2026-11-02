import { lstat, readFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { parseArgs } from 'node:util';
import { inspectAppStartupFile, verifyStartupBuild } from './app-startup-files.ts';

try {
  const { values } = parseArgs({ options: { directory: { type: 'string' }, manifest: { type: 'string' },
    exe: { type: 'string' }, source: { type: 'string' }, collection: { type: 'string' }, 'build-only': { type: 'boolean' } } });
  if (!values.manifest || !values.exe || !values.source || (!values['build-only'] && !values.directory)) throw new Error();
  const manifestStat = await lstat(values.manifest); const exeStat = await lstat(values.exe);
  if (!manifestStat.isFile() || manifestStat.isSymbolicLink() || manifestStat.size > 2048
    || !exeStat.isFile() || exeStat.isSymbolicLink() || exeStat.size > 64 * 1024 * 1024) throw new Error();
  const manifestText = await readFile(values.manifest, 'utf8');
  if (Buffer.byteLength(manifestText) > 2048) throw new Error();
  const manifest: unknown = JSON.parse(manifestText);
  const bytes = await readFile(values.exe);
  if (bytes.length > 64 * 1024 * 1024) throw new Error();
  const exeSha256 = createHash('sha256').update(bytes).digest('hex');
  if (JSON.stringify(manifest) !== manifestText || !verifyStartupBuild(manifest, values.source, exeSha256)) throw new Error();
  // validator通過済みの固定情報だけを出力する。例外/path/原JSONは出さない。
  const build = { schema: 1, variant: manifest.variant, sourceCommit: values.source,
    exeSha256, profile: manifest.profile, features: ['custom-protocol', 'startup-diagnostics'], distribution: false,
    ...(manifest.wryTrace ? { wryTrace: manifest.wryTrace } : {}) };
  if (values['build-only']) console.log(JSON.stringify({ code: 'APP_STARTUP_BUILD_VERIFIED', build }));
  else {
    let collection: unknown;
    try {
      if (values.collection) {
        const stat = await lstat(values.collection);
        if (stat.isFile() && !stat.isSymbolicLink() && stat.size <= 65536) {
          const text = await readFile(values.collection, 'utf8');
          if (Buffer.byteLength(text) <= 65536) collection = JSON.parse(text);
        }
      }
    } catch { /* 回収証跡欠測はincomplete。原文/例外を出さない。 */ }
    const startup = await inspectAppStartupFile(values.directory ?? '', collection);
    const complete = startup.phase !== 'diagnostic-incomplete'
      && (manifest.variant !== 'host-wry-startup-diagnostic-1' || ('wryHook' in startup && startup.wryHook === true));
    console.log(JSON.stringify({ code: complete ? 'APP_STARTUP_COMPLETE' : 'APP_STARTUP_INCOMPLETE',
      diagnostic: complete ? 'complete' : 'incomplete', build, startup }));
    process.exitCode = complete ? 0 : 1;
  }
} catch {
  console.log(JSON.stringify({ code: 'APP_STARTUP_BUILD_REJECTED', diagnostic: 'incomplete' }));
  process.exitCode = 1;
}
