import { test } from 'node:test';
import assert from 'node:assert/strict';
import { browser, url } from './app-browser.ts';
void test('resident controls use shared IPC and wait for applied observations', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 1000 } });
  await page.addInitScript(() => {
    let revision = 1, on = false;
    let registration = 'disconnected';
    let transition: { requestId: number; intent: 'on' | 'off'; outcome: 'applied' } | null = null;
    window.__TAURI__ = { core: { invoke: (command, args) => {
      if (command === 'get_status_presentation') return Promise.resolve({ protocolVersion: 1, source: 'mac-status', state: 'source-unavailable' });
      if (command === 'get_resident_status') return Promise.resolve({ protocolVersion: 1, source: 'resident-owner', state: 'source-observation',
        revision, validForMs: 3000, standby: on ? 'on' : 'off', registration, caller: null,
        work: 'idle', cleanup: 'confirmed', canRequestOn: !on, canRequestOff: on, transition,
        standbyUntil: on ? 1_900_000_000_000 : null, standbyEndingSoon: false, standbyExpired: false });
      if (command !== 'request_resident_intent') return Promise.reject(new Error('unexpected operation'));
      if (typeof args.request !== 'string') return Promise.reject(new Error('expected an intent document'));
      const request = JSON.parse(args.request) as { requestId: number; expectedRevision: number; intent: 'on' | 'off' };
      if (request.expectedRevision !== revision || request.requestId !== (on ? 2 : 1)) return Promise.reject(new Error('invalid request'));
      document.documentElement.dataset.residentCalls = String(request.requestId);
      setTimeout(() => { on = request.intent === 'on'; registration = on ? 'connecting' : 'disconnected';
        if (on) setTimeout(() => { registration = 'authenticated'; revision++; }, 800);
        revision++; transition = { requestId: request.requestId, intent: request.intent, outcome: 'applied' }; }, 300);
      return Promise.resolve({ protocolVersion: 1, requestId: request.requestId, intent: request.intent, disposition: 'accepted' });
    } } };
  });
  await page.goto(url);
  await page.waitForFunction(() => document.querySelector<HTMLButtonElement>('#resident-on')?.disabled === false);
  await page.locator('#resident-on').click();
  assert.equal(await page.locator('#resident-off').isDisabled(), true);
  assert.match(await page.locator('#resident-heading').innerText(), /変更を確認中/);
  await page.waitForFunction(() => document.querySelector<HTMLButtonElement>('#resident-off')?.disabled === false);
  assert.match(await page.locator('#resident-heading').innerText(), /ONを確認/);
  assert.match(await page.locator('#resident-state').innerText(), /接続: 接続を確認中/);
  await page.waitForFunction(() => document.querySelector('#resident-panel')?.getAttribute('data-resident-registration') === 'authenticated');
  assert.match(await page.locator('#resident-state').innerText(), /接続: 認証済み/);
  await page.locator('#resident-off').click();
  await page.waitForFunction(() => document.querySelector<HTMLButtonElement>('#resident-on')?.disabled === false);
  assert.match(await page.locator('#resident-heading').innerText(), /OFFを確認/);
  assert.match(await page.locator('#resident-state').innerText(), /接続: 未接続/);
  assert.equal(await page.locator('html').getAttribute('data-resident-calls'), '2');
  await page.setViewportSize({ width: 320, height: 1000 });
  await page.evaluate(() => { document.documentElement.style.fontSize = '200%'; });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  // 待機ONを許可と誤解させない1文だけを残す。
  const panel = await page.locator('#resident-panel').innerText();
  assert.match(panel, /待機ONは作業の許可ではありません。/);
  assert.doesNotMatch(panel, /受付は変更完了/);
  await page.close();
});
void test('cleanup unknown disables resident controls and is shown as needs cleanup', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    window.__TAURI__ = { core: { invoke: command => Promise.resolve(command === 'get_status_presentation'
      ? { protocolVersion: 1, source: 'mac-status', state: 'source-unavailable' }
      : { protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision: 1, validForMs: 3000,
        standby: 'unknown', registration: 'unknown', caller: 'private-fixture-name', work: 'unknown', cleanup: 'unknown',
        canRequestOn: false, canRequestOff: false, transition: null, standbyUntil: null, standbyEndingSoon: false, standbyExpired: false }) } };
  });
  await page.goto(url); await page.waitForSelector('#resident-panel');
  assert.equal(await page.locator('#resident-on').isDisabled(), true); assert.equal(await page.locator('#resident-off').isDisabled(), true);
  assert.equal(await page.locator('#resident-heading').innerText(), '要清掃: 常駐ホストの回収を確認できません');
  assert.equal(await page.locator('#resident-state').innerText(), '回収を確かめられないため、停止の成功を示しません。');
  assert.equal(await page.locator('#resident-diagnostic').innerText(), 'RESIDENT_CLEANUP_UNKNOWN');
  assert.doesNotMatch(await page.locator('body').innerText(), /private-fixture-name/);
  await page.close();
});
