import { after } from 'node:test';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { readFile, readdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { chromium } from 'playwright-core';

const root = fileURLToPath(new URL('../', import.meta.url));
const config = JSON.parse(await readFile(path.join(root, 'src-tauri/tauri.conf.json'), 'utf8')) as { app: { security: { csp: string } } };
const assets = new Set(['index.html', 'THIRD_PARTY_NOTICES.txt', ...(await readdir(path.join(root, 'dist/assets'))).map(name => `assets/${name}`)]);
const server = createServer((request, response) => {
  void (async () => {
  const name = new URL(request.url ?? '/', 'http://127.0.0.1').pathname.slice(1) || 'index.html';
  if (!assets.has(name)) { response.writeHead(404).end(); return; }
  const type = name.endsWith('.css') ? 'text/css' : name.endsWith('.js') ? 'text/javascript' : 'text/html';
  response.writeHead(200, { 'Content-Type': type, 'Content-Security-Policy': config.app.security.csp });
  response.end(await readFile(path.join(root, 'dist', name)));
  })().catch(() => { response.writeHead(500).end(); });
});
await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
export const browser = await chromium.launch({ channel: 'chrome', headless: true });
export const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
after(async () => { await browser.close(); await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve())); });
