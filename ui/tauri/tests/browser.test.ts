import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { readFile, mkdir, readdir } from 'node:fs/promises';
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
const browser = await chromium.launch({ channel: 'chrome', headless: true });
const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
after(async () => { await browser.close(); await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve())); });

void test('browserではRust未取得を明示し、keyboard・focus・拡大でも無効操作と診断を保つ', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 1000 } });
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector('#status-title')?.textContent === '状態を取得できません');
  assert.match(await page.locator('#status-description').innerText(), /ブラウザー表示/);
  const status = page.getByRole('status');
  assert.equal(await status.count(), 1);
  assert.equal(await status.getAttribute('aria-live'), 'polite');
  assert.equal(await status.getAttribute('aria-atomic'), 'true');
  assert.equal(await status.locator('h2#status-title').count(), 1);
  assert.equal(await status.locator('p#status-description').count(), 1);
  assert.equal(await status.locator('svg[aria-hidden="true"][focusable="false"]').count(), 1);
  assert.equal(await status.getByRole('button').count(), 0);
  assert.equal(await page.getByRole('button', { name: 'サポートを開始' }).isDisabled(), true);
  assert.equal(await page.getByRole('button', { name: '接続を停止' }).isDisabled(), true);
  await page.keyboard.press('Tab');
  assert.equal(await page.locator(':focus').getAttribute('aria-expanded'), 'false');
  await page.keyboard.press('Enter');
  assert.equal(await page.getByRole('button', { name: '診断情報', exact: true }).getAttribute('aria-expanded'), 'true');
  await page.keyboard.press('Tab');
  assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
  assert.equal(await page.locator(':focus').evaluate(node => getComputedStyle(node).outlineWidth), '3px');
  await page.keyboard.press('Enter');
  assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
  for (const [name, width, fontSize] of [['wide', 1200, '100%'], ['narrow', 320, '100%'], ['text-200', 1200, '200%']] as const) {
    await page.setViewportSize({ width, height: 1000 });
    await page.evaluate(value => { document.documentElement.style.fontSize = value ?? '100%'; }, fontSize);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    if (process.env.SHELL_SCREENSHOT_DIR) {
      await mkdir(process.env.SHELL_SCREENSHOT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.SHELL_SCREENSHOT_DIR, `shell-browser-${name}.png`), fullPage: true });
    }
  }
  await page.emulateMedia({ reducedMotion: 'reduce', forcedColors: 'active' });
  assert.equal(await page.locator('#refresh').evaluate(node => getComputedStyle(node).transitionDuration), '0s');
  assert.equal(await page.locator('.status').evaluate(node => getComputedStyle(node).borderStyle), 'solid');
  assert.deepEqual(errors, []);
  await page.close();
});

void test('未配線IPC fixtureは接続成功にせず、HTML風不正応答も解釈しない', async () => {
  for (const valid of [true, false]) {
    const page = await browser.newPage();
    await page.addInitScript(valid => {
      window.__TAURI__ = { core: { invoke: command => Promise.resolve(command === 'get_status_presentation' ? { protocolVersion: 1, source: 'windows-shell' } : { protocolVersion: 1,
        integration: valid ? 'not_connected' : '<img src=x onerror="window.injected=true">',
        canStart: false, canStop: false, termination: 'unconfirmed' }) } };
    }, valid);
    await page.goto(url);
    await page.waitForFunction(() => Boolean(document.querySelector('#status-title')) && document.querySelector('#status-title')?.textContent !== '状態を確認中');
    assert.equal(await page.locator('#status-title').innerText(), valid ? 'バックエンド未接続' : '状態を取得できません');
    assert.equal(await page.getByRole('button', { name: 'サポートを開始' }).isDisabled(), true);
    if (valid) assert.match(await page.locator('#status-description').innerText(), /現在の shell は未配線/);
    assert.equal(await page.getByRole('button', { name: '接続を停止' }).isDisabled(), true);
    assert.equal(await page.locator('img').count(), 0);
    assert.equal(await page.evaluate(() => (window as Window & { injected?: unknown }).injected), undefined);
    await page.close();
  }
});

void test('Rust待機中にも診断を開き、browser画面を破棄できる', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => { window.__TAURI__ = { core: { invoke: () => new Promise(() => {}) } }; });
  await page.goto(url);
  await page.waitForSelector('#status-title');
  assert.equal(await page.locator('#status-title').innerText(), '状態を確認中');
  await page.getByText('診断情報', { exact: true }).click();
  assert.equal(await page.getByRole('button', { name: '診断情報', exact: true }).getAttribute('aria-expanded'), 'true');
  await page.close();
  assert.equal(page.isClosed(), true);
});


void test('診断開閉は名前と展開状態を持ち、停止はLucide SVGと可視文言を持つ', async () => {
  const page = await browser.newPage();
  await page.goto(url);
  const trigger = page.getByRole('button', { name: '診断情報', exact: true });
  assert.equal(await trigger.count(), 1);
  assert.equal(await trigger.getAttribute('aria-expanded'), 'false');
  await trigger.press('Space');
  assert.equal(await trigger.getAttribute('aria-expanded'), 'true');
  const stop = page.getByRole('button', { name: '接続を停止', exact: true });
  assert.equal(await stop.isDisabled(), true);
  assert.equal(await stop.locator('svg[aria-hidden="true"][focusable="false"]').count(), 1);
  assert.equal(/\p{RGI_Emoji}/v.test(await page.locator('body').innerText()), false);
  await page.close();
});

void test('再確認の待機中もfocusと診断を保ち、重複要求と遅延成功表示を防ぐ', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    let calls = 0;
    window.__TAURI__ = { core: { invoke: command => {
      if (command === 'get_status_presentation') return Promise.resolve({ protocolVersion: 1, source: 'windows-shell' });
      calls += 1;
      document.documentElement.dataset.calls = String(calls);
      return calls === 1 ? Promise.resolve({ protocolVersion: 1, integration: 'not_connected', canStart: false, canStop: false, termination: 'unconfirmed' }) : new Promise(() => {});
    } } };
  });
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector('#status-title')?.textContent === 'バックエンド未接続');
  await page.getByRole('button', { name: '診断情報', exact: true }).press('Enter');
  const refresh = page.getByRole('button', { name: '状態を再確認' });
  await refresh.press('Enter');
  assert.equal(await refresh.getAttribute('aria-disabled'), 'true');
  assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
  await refresh.press('Space');
  assert.equal(await page.locator('html').getAttribute('data-calls'), '2');
  await page.emulateMedia({ reducedMotion: 'reduce' });
  assert.equal(await page.locator('.icon-spin').evaluate(node => getComputedStyle(node).animationName), 'none');
  await page.waitForFunction(() => document.querySelector('#diagnostic')?.textContent === 'IPC_TIMEOUT');
  assert.equal(await page.getByRole('button', { name: '接続を停止' }).isDisabled(), true);
  assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
  await page.close();
});

void test('browserで逆順focusと診断の反復開閉を保ち、完了した再確認を繰り返せる', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    let calls = 0;
    window.__TAURI__ = { core: { invoke: command => {
      if (command === 'get_status_presentation') return Promise.resolve({ protocolVersion: 1, source: 'windows-shell' });
      document.documentElement.dataset.calls = String(++calls);
      return Promise.resolve({ protocolVersion: 1, integration: 'not_connected', canStart: false, canStop: false, termination: 'unconfirmed' });
    } } };
  });
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector('#status-title')?.textContent === 'バックエンド未接続');
  const trigger = page.getByRole('button', { name: '診断情報', exact: true });
  const refresh = page.getByRole('button', { name: '状態を再確認' });
  await page.keyboard.press('Tab');
  await page.keyboard.press('Space');
  for (const [index, activation] of ['Enter', 'Space', 'Enter'].entries()) {
    await page.keyboard.press('Tab');
    assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
    await page.keyboard.press('Shift+Tab');
    assert.equal(await trigger.evaluate(node => node === document.activeElement), true);
    await page.keyboard.press('Space');
    assert.equal(await trigger.getAttribute('aria-expanded'), 'false');
    assert.equal(await refresh.count(), 0);
    assert.equal(await trigger.evaluate(node => node === document.activeElement), true);
    await page.keyboard.press('Enter');
    assert.equal(await trigger.getAttribute('aria-expanded'), 'true');
    await page.keyboard.press('Tab');
    await page.keyboard.press(activation);
    await page.waitForFunction(expected => document.documentElement.dataset.calls === String(expected) && document.querySelector('#diagnostic')?.textContent === 'SHELL_UNWIRED', index + 2);
    assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
    assert.equal(await refresh.evaluate(node => node.matches(':focus-visible') && parseFloat(getComputedStyle(node).outlineWidth) >= 1), true);
    assert.equal(await page.getByRole('button', { name: 'サポートを開始' }).isDisabled(), true);
    assert.equal(await page.getByRole('button', { name: '接続を停止' }).isDisabled(), true);
    await page.keyboard.press('Shift+Tab');
  }
  assert.deepEqual(await page.locator('.shell-permissions dd').allTextContents(), ['未確認', '未確認', '未確認', '未確認']);
  await page.close();
});

void test('browserの取得失敗後もfocusを失わずkeyboardで再確認して回復できる', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    let calls = 0;
    window.__TAURI__ = { core: { invoke: command => command === 'get_status_presentation' ? Promise.resolve({ protocolVersion: 1, source: 'windows-shell' }) : ++calls === 2 ? Promise.reject(new Error('fixture-secret-detail'))
      : Promise.resolve({ protocolVersion: 1, integration: 'not_connected', canStart: false, canStop: false, termination: 'unconfirmed' }) } };
  });
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector('#status-title')?.textContent === 'バックエンド未接続');
  await page.keyboard.press('Tab');
  await page.keyboard.press('Enter');
  await page.keyboard.press('Tab');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.querySelector('#diagnostic')?.textContent === 'IPC_ERROR');
  assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
  assert.equal(await page.locator('body').innerText().then(text => text.includes('fixture-secret-detail')), false);
  assert.equal(await page.locator('#status-title').innerText(), '状態を取得できません');
  await page.keyboard.press('Space');
  await page.waitForFunction(() => document.querySelector('#diagnostic')?.textContent === 'SHELL_UNWIRED');
  assert.equal(await page.locator(':focus').getAttribute('id'), 'refresh');
  assert.equal(await page.locator('#status-title').innerText(), 'バックエンド未接続');
  assert.equal(await page.getByRole('button', { name: '接続を停止' }).isDisabled(), true);
  await page.close();
});

void test('readerの終了未確認コードは狭い拡大表示でも診断内で読める', async () => {
  const page = await browser.newPage({ viewport: { width: 320, height: 1000 } });
  await page.addInitScript(() => {
    window.__TAURI__ = { core: {
      // Native Tauri Err(&str) is serialized as a string rejection.
      // eslint-disable-next-line @typescript-eslint/prefer-promise-reject-errors
      invoke: command => command === 'get_status_presentation' ? Promise.resolve({ protocolVersion: 1, source: 'windows-shell' }) : Promise.reject('TERMINATION_UNCONFIRMED')
    } };
  });
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector('#status-title')?.textContent === '回収を確認できません');
  assert.match(await page.locator('#status-description').innerText(), /EOF や画面終了は回収確認ではありません/);
  await page.getByRole('button', { name: '診断情報', exact: true }).click();
  await page.waitForFunction(() => document.querySelector('#diagnostic')?.textContent === 'TERMINATION_UNCONFIRMED');
  await page.evaluate(() => { document.documentElement.style.fontSize = '200%'; });
  assert.equal(await page.locator('#status-title').innerText(), '回収を確認できません');
  assert.equal(await page.getByRole('button', { name: 'サポートを開始' }).isDisabled(), true);
  assert.equal(await page.getByRole('button', { name: '接続を停止', exact: true }).isDisabled(), true);
  if (process.env.SHELL_SCREENSHOT_DIR) {
    await mkdir(process.env.SHELL_SCREENSHOT_DIR, { recursive: true });
    await page.screenshot({ path: path.join(process.env.SHELL_SCREENSHOT_DIR, 'reader-fault-narrow200.png'), fullPage: true });
  }
  assert.equal(await page.locator('#diagnostic').evaluate(node => node.scrollWidth <= node.clientWidth), true);
  if (process.env.SHELL_SCREENSHOT_DIR) {
    await page.setViewportSize({ width: 900, height: 1000 });
    await page.evaluate(() => { document.documentElement.style.fontSize = '100%'; });
    await page.screenshot({ path: path.join(process.env.SHELL_SCREENSHOT_DIR, 'reader-fault-wide.png'), fullPage: true });
  }
  await page.close();
});
