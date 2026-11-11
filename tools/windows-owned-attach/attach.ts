// Separate experiment caller. Never imported by the established native smoke.
import { writeFile } from 'node:fs/promises';
import path from 'node:path';
import { Driver } from '../../ui/tauri/native-smoke/webdriver.ts';
const output = process.env.OWNED_ATTACH_OUTPUT;
try {
  if (process.platform !== 'win32' || !output) throw new Error('OWNED_ATTACH_ENVIRONMENT');
  const driver = new Driver('http://127.0.0.1:4444');
  // UseWebView maps to browserName=webview2 in Microsoft EdgeOptions. No binary,
  // application, tauri:options, or profile capability that could launch another app.
  const value = await driver.request('POST', '/session', { capabilities: { alwaysMatch: {
    browserName: 'webview2', 'ms:edgeChromium': true,
    'ms:edgeOptions': { debuggerAddress: '127.0.0.1:9222' }
  } } });
  if (!value || typeof value !== 'object' || !('sessionId' in value) || typeof value.sessionId !== 'string'
      || !('capabilities' in value) || !value.capabilities || typeof value.capabilities !== 'object'
      || !('browserVersion' in value.capabilities) || typeof value.capabilities.browserVersion !== 'string') {
    throw new Error('OWNED_ATTACH_INVALID_SESSION');
  }
  await writeFile(path.join(output, 'session.private.json'), JSON.stringify({ session: encodeURIComponent(value.sessionId), browserVersion: value.capabilities.browserVersion }), { flag: 'wx' });
} catch {
  console.error('OWNED_ATTACH_SESSION_FAILED'); process.exitCode = 1;
}
