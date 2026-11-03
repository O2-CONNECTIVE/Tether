import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { readFile, readdir, mkdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { chromium } from 'playwright-core';

const root = fileURLToPath(new URL('../', import.meta.url));
const config = JSON.parse(await readFile(path.join(root, 'src-tauri/tauri.conf.json'), 'utf8')) as { app: { security: { csp: string } } };
const assets = new Set(['index.html', ...(await readdir(path.join(root, 'dist/assets'))).map(name => `assets/${name}`)]);
const server = createServer((request, response) => {
  void (async () => {
    const name = new URL(request.url ?? '/', 'http://127.0.0.1').pathname.slice(1) || 'index.html';
    if (!assets.has(name)) { response.writeHead(404).end(); return; }
    response.writeHead(200, { 'Content-Type': name.endsWith('.css') ? 'text/css' : name.endsWith('.js') ? 'text/javascript' : 'text/html', 'Content-Security-Policy': config.app.security.csp });
    response.end(await readFile(path.join(root, 'dist', name)));
  })().catch(() => { response.writeHead(500).end(); });
});
await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
const browser = await chromium.launch({ channel: 'chrome', headless: true });
const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
after(async () => { await browser.close(); await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve())); });

void test('通常AppはbackendのMac経路を読み、実入力未接続を表示する', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    window.__TAURI__ = { core: { invoke: (command, args) => {
      document.documentElement.dataset.command = command;
      document.documentElement.dataset.request = JSON.stringify(args);
      return Promise.resolve({ protocolVersion: 1, source: 'mac-status', state: 'source-unavailable' });
    } } };
  });
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector('#status-title')?.textContent !== '状態を確認中');
  assert.equal(await page.locator('html').getAttribute('data-command'), 'get_status_presentation');
  assert.equal(await page.locator('html').getAttribute('data-request'), '{"request":{"protocolVersion":1}}');
  assert.equal(await page.locator('#status-title').innerText(), 'Mac の状態取得は未接続です');
  assert.equal(await page.getByRole('button', { name: 'サポートを開始' }).isDisabled(), true);
  assert.equal(await page.getByRole('button', { name: '接続を停止', exact: true }).isDisabled(), true);
  assert.equal(await page.locator('#synthetic-observation').count(), 0);
  await page.close();
});

void test('合成の通常App表示は明示ラベルを持ち、期限後と再確認で進行を復活しない', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    let calls = 0;
    window.__TAURI__ = { core: { invoke: command => {
      document.documentElement.dataset.calls = String(++calls);
      document.documentElement.dataset.command = command;
      return Promise.resolve({ protocolVersion: 1, source: 'mac-status', state: 'synthetic-observation',
        view: { heading: '接続を確認中', detail: '閉じた表示試験です。', icon: 'checking' },
        validForMs: 1200, cleanupUnknown: false, terminalHistory: false });
    } } };
  });
  await page.goto(url);
  await page.waitForSelector('#synthetic-observation');
  assert.equal(await page.locator('#synthetic-observation').innerText(), '表示テスト・実接続なし');
  assert.equal(await page.locator('#status-title').innerText(), '接続を確認中');
  assert.equal(await page.locator('.icon-spin').count(), 1);
  await page.getByRole('button', { name: '診断情報', exact: true }).press('Enter');
  await page.getByRole('button', { name: '状態を再確認', exact: true }).focus();
  await page.waitForFunction(() => document.querySelector('#diagnostic')?.textContent === 'MAC_OBSERVATION_EXPIRED');
  assert.equal(await page.locator('.icon-spin').count(), 0);
  assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
  await page.keyboard.press('Enter');
  assert.equal(await page.locator('html').getAttribute('data-calls'), '1');
  assert.equal(await page.locator('#status-title').innerText(), '状態の更新を確認できません');
  assert.equal(await page.getByRole('button', { name: '接続を停止', exact: true }).isDisabled(), true);
  await page.close();
});

void test('合成のunknown警告とEOF履歴は狭幅拡大でも現在の回収証明にしない', async () => {
  for (const unknown of [true, false]) {
    const page = await browser.newPage({ viewport: { width: 320, height: 1000 } });
    await page.addInitScript(unknown => {
      window.__TAURI__ = { core: { invoke: () => Promise.resolve({ protocolVersion: 1, source: 'mac-status', state: 'synthetic-observation',
        view: unknown ? { heading: '回収を確認できません', detail: '操作が終わっていても、安全に回収されたとは限りません。', icon: 'warning' }
          : { heading: '状態配信終了・最終報告の履歴', detail: '最終報告：回収確認済み。現在の接続や OS 全体の回収を独立に確認したものではありません。', icon: 'neutral' },
        validForMs: 0, cleanupUnknown: unknown, terminalHistory: !unknown }) } };
    }, unknown);
    await page.goto(url);
    await page.waitForSelector('#synthetic-observation');
    assert.equal(await page.locator('#status-title').innerText(), unknown ? '回収を確認できません' : '状態の更新を確認できません');
    assert.equal(await page.locator('#status-history').count(), unknown ? 0 : 1);
    if (!unknown) assert.match(await page.locator('#status-history').innerText(), /最終報告の履歴/);
    await page.getByRole('button', { name: '診断情報', exact: true }).press('Enter');
    await page.getByRole('button', { name: '状態を再確認', exact: true }).press('Enter');
    await page.evaluate(() => { document.documentElement.style.fontSize = '200%'; });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.emulateMedia({ forcedColors: 'active', reducedMotion: 'reduce' });
    assert.equal(await page.locator('.status').evaluate(node => getComputedStyle(node).borderStyle), 'solid');
    assert.equal(await page.getByRole('button', { name: 'サポートを開始' }).isDisabled(), true);
    if (process.env.SHELL_SCREENSHOT_DIR) {
      await mkdir(process.env.SHELL_SCREENSHOT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.SHELL_SCREENSHOT_DIR, unknown ? 'mac-unknown-narrow200.png' : 'mac-history-narrow200.png'), fullPage: true });
      await page.setViewportSize({ width: 900, height: 1000 });
      await page.evaluate(() => { document.documentElement.style.fontSize = '100%'; });
      await page.emulateMedia({ forcedColors: 'none' });
      await page.screenshot({ path: path.join(process.env.SHELL_SCREENSHOT_DIR, unknown ? 'mac-unknown-wide.png' : 'mac-history-wide.png'), fullPage: true });
    }
    await page.close();
  }
});

void test('画面ライフサイクル無効化後の古い成功は通常Appへ届かない', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    window.__TAURI__ = { core: { invoke: () => new Promise(resolve => {
      setTimeout(() => resolve({ protocolVersion: 1, source: 'mac-status', state: 'synthetic-observation',
        view: { heading: '接続を確認中', detail: 'late fixture', icon: 'checking' },
        validForMs: 3000, cleanupUnknown: false, terminalHistory: false }), 500);
    }) } };
  });
  await page.goto(url);
  await page.waitForSelector('#status-title');
  await page.evaluate(() => { document.dispatchEvent(new Event('visibilitychange')); });
  await page.waitForTimeout(600);
  assert.equal(await page.locator('#status-title').innerText(), '状態の更新を確認できません');
  assert.equal(await page.locator('#synthetic-observation').count(), 0);
  assert.equal(await page.locator('.icon-spin').count(), 0);
  await page.close();
});

void test('pagehide後の復帰でもfixtureの進行表示を停止したままにする', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    window.__TAURI__ = { core: { invoke: () => Promise.resolve({ protocolVersion: 1, source: 'mac-status', state: 'synthetic-observation',
      view: { heading: '接続を確認中', detail: '閉じた表示試験です。', icon: 'checking' },
      validForMs: 3000, cleanupUnknown: false, terminalHistory: false }) } };
  });
  await page.goto(url);
  await page.waitForSelector('#synthetic-observation');
  await page.evaluate(() => {
    window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }));
    window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
  });
  assert.equal(await page.locator('#status-title').innerText(), '状態の更新を確認できません');
  assert.equal(await page.locator('.icon-spin').count(), 0);
  await page.close();
});
