import { test } from 'node:test';
import assert from 'node:assert/strict';
import { browser, url } from './app-browser.ts';
void test('resident switch and deadline use shared IPC and wait for owner observations', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 1000 }, timezoneId: 'Asia/Tokyo' });
  await page.addInitScript(() => {
    let revision = 1, on = false, requestId = 0, deadline = 1_900_000_000_000, endingSoon = true;
    let registration = 'disconnected';
    let transition: { requestId: number; intent: 'on' | 'off'; outcome: 'applied' } | null = null;
    window.__TAURI__ = { core: { invoke: (command, args) => {
      if (command === 'get_status_presentation') return Promise.resolve({ protocolVersion: 1, source: 'mac-status', state: 'source-unavailable' });
      if (command === 'get_resident_status') {
        const status = { protocolVersion: 1, source: 'resident-owner', state: 'source-observation',
          revision, validForMs: 3000, standby: on ? 'on' : 'off', registration, caller: null,
          work: 'idle', cleanup: 'confirmed', canRequestOn: !on, canRequestOff: on, transition,
          standbyUntil: on ? deadline : null, standbyEndingSoon: on && endingSoon, standbyExpired: false, sleepPrevention: 'inactive' };
        if (document.documentElement.dataset.holdPoll !== 'true') return Promise.resolve(status);
        document.documentElement.dataset.pollHeld = 'true';
        return new Promise(resolve => { window.addEventListener('test-release-poll', () => resolve(status), { once: true }); });
      }
      if (command !== 'request_resident_intent') return Promise.reject(new Error('unexpected operation'));
      if (typeof args.request !== 'string') return Promise.reject(new Error('expected an intent document'));
      const request = JSON.parse(args.request) as { requestId: number; expectedRevision: number; intent: 'on' | 'off' | 'extend'; standbyHours?: number };
      if (request.expectedRevision !== revision || request.requestId !== ++requestId) return Promise.reject(new Error('invalid request'));
      document.documentElement.dataset.lastResidentIntent = `${request.intent}:${request.standbyHours ?? 'none'}`;
      const { intent } = request;
      if (intent === 'extend' && requestId === 3) {
        return Promise.resolve({ protocolVersion: 1, requestId, intent, disposition: 'rejected' });
      }
      if (intent === 'extend') {
        window.addEventListener('test-renew-deadline', () => { deadline = 1_900_007_200_000; endingSoon = false; revision++; }, { once: true });
      } else {
        window.addEventListener('test-apply-standby', () => {
          on = intent === 'on';
          registration = on ? 'connecting' : 'disconnected';
          if (on) window.addEventListener('test-authenticate-resident', () => {
            registration = 'authenticated'; revision++;
          }, { once: true });
          revision++; transition = { requestId: request.requestId, intent, outcome: 'applied' };
        }, { once: true });
      }
      return Promise.resolve({ protocolVersion: 1, requestId: request.requestId, intent: request.intent, disposition: 'accepted' });
    } } };
  });
  await page.goto(url);
  const standby = page.getByRole('switch', { name: '待機', exact: true });
  await standby.waitFor();
  assert.equal(await standby.getAttribute('aria-checked'), 'false');
  assert.equal(await page.getByLabel('待機時間').isVisible(), false);
  const notice = page.locator('.resident-standby__notice[role="status"]');
  assert.equal(await notice.count(), 1);
  await page.getByText('詳細設定', { exact: true }).click();
  assert.equal(await page.getByLabel('待機時間').inputValue(), '2');
  await page.getByLabel('待機時間').selectOption('4');
  await page.waitForSelector('#resident-switch[aria-disabled="false"]');
  await standby.press('Space');
  await page.waitForSelector('html[data-last-resident-intent="on:4"]');
  assert.equal(await standby.isDisabled(), true);
  assert.equal(await standby.getAttribute('aria-checked'), 'false');
  assert.match(await page.locator('#resident-heading').innerText(), /変更を確認中/);
  await page.evaluate(() => window.dispatchEvent(new Event('test-apply-standby')));
  await page.waitForSelector('#resident-switch[aria-checked="true"][aria-disabled="false"]');
  assert.equal(await page.locator('html').getAttribute('data-last-resident-intent'), 'on:4');
  assert.match(await page.locator('#resident-heading').innerText(), /ONを確認/);
  assert.match(await page.locator('#resident-state').innerText(), /接続: 接続を確認中/);
  await page.evaluate(() => window.dispatchEvent(new Event('test-authenticate-resident')));
  await page.waitForFunction(() => document.querySelector('#resident-panel')?.getAttribute('data-resident-registration') === 'authenticated');
  assert.match(await page.locator('#resident-state').innerText(), /接続: 認証済み/);
  const deadline = page.locator('#resident-panel time');
  const previous = new Date(1_900_000_000_000).toISOString();
  const renewed = new Date(1_900_007_200_000).toISOString();
  assert.equal(await deadline.getAttribute('datetime'), previous);
  assert.match(await deadline.innerText(), /\d+時\d{2}分まで/);
  assert.equal(await page.getByText('待機期限まで残り10分以内です。').isVisible(), true);
  await page.getByRole('button', { name: '今から4時間に延長' }).click();
  assert.equal(await page.locator('html').getAttribute('data-last-resident-intent'), 'extend:4');
  assert.equal(await deadline.getAttribute('datetime'), previous);
  await page.evaluate(() => window.dispatchEvent(new Event('test-renew-deadline')));
  await page.waitForSelector(`#resident-panel time[datetime="${renewed}"]`);
  assert.equal(await page.getByText('待機期限まで残り10分以内です。').isVisible(), false);
  await page.getByRole('button', { name: '今から4時間に更新' }).click();
  await notice.getByText('期限を更新できませんでした。表示された期限を確認してください。').waitFor();
  await page.evaluate(() => { document.documentElement.dataset.holdPoll = 'true'; });
  await page.waitForFunction(() => document.documentElement.dataset.pollHeld === 'true');
  assert.equal(await standby.isDisabled(), true);
  assert.match(await notice.innerText(), /期限を更新できませんでした/);
  await page.evaluate(() => {
    document.documentElement.dataset.holdPoll = 'false';
    window.dispatchEvent(new Event('test-release-poll'));
  });
  await standby.click();
  await page.waitForSelector('html[data-last-resident-intent="off:none"]');
  await page.evaluate(() => window.dispatchEvent(new Event('test-apply-standby')));
  await page.waitForSelector('#resident-switch[aria-checked="false"][aria-disabled="false"]');
  assert.match(await page.locator('#resident-heading').innerText(), /OFFを確認/);
  assert.match(await page.locator('#resident-state').innerText(), /接続: 未接続/);
  assert.equal(await page.locator('html').getAttribute('data-last-resident-intent'), 'off:none');
  await page.setViewportSize({ width: 320, height: 1000 });
  await page.evaluate(() => { document.documentElement.style.fontSize = '200%'; });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  // 待機ONを許可と誤解させない1文だけを残す。
  const panel = await page.locator('#resident-panel').innerText();
  assert.match(panel, /待機ONは作業の許可ではありません。/);
  assert.doesNotMatch(panel, /受付は変更完了/);
  await page.reload();
  await page.getByText('詳細設定', { exact: true }).click();
  assert.equal(await page.getByLabel('待機時間').inputValue(), '2');
  await page.close();
});
void test('cleanup unknown disables resident controls and is shown as needs cleanup', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    window.__TAURI__ = { core: { invoke: command => Promise.resolve(command === 'get_status_presentation'
      ? { protocolVersion: 1, source: 'mac-status', state: 'source-unavailable' }
      : { protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision: 1, validForMs: 3000,
        standby: 'unknown', registration: 'unknown', caller: 'private-fixture-name', work: 'unknown', cleanup: 'unknown',
        canRequestOn: false, canRequestOff: false, transition: null, standbyUntil: null, standbyEndingSoon: false, standbyExpired: true,
        sleepPrevention: 'unknown' }) } };
  });
  await page.goto(url); await page.waitForSelector('#resident-panel');
  assert.equal(await page.locator('#resident-switch').isDisabled(), true);
  assert.equal(await page.locator('#resident-switch').getAttribute('aria-checked'), null);
  assert.equal(await page.locator('#resident-heading').innerText(), '要清掃: 常駐ホストの回収を確認できません');
  assert.equal(await page.locator('#resident-state').innerText(), '回収を確かめられないため、停止の成功を示しません。');
  assert.equal(await page.locator('#resident-diagnostic').innerText(), 'RESIDENT_CLEANUP_UNKNOWN');
  assert.doesNotMatch(await page.locator('body').innerText(), /private-fixture-name/);
  await page.close();
});

void test('sleep prevention and the lid note use fresh owner status', async () => {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    let revision = 1, sleepPrevention = 'active', available = true;
    window.addEventListener('test-sleep-prevention', event => {
      sleepPrevention = (event as CustomEvent<string>).detail; revision++;
    });
    window.addEventListener('test-lose-resident-status', () => { available = false; });
    window.__TAURI__ = { core: { invoke: command => Promise.resolve(command === 'get_status_presentation'
      ? { protocolVersion: 1, source: 'mac-status', state: 'source-unavailable' }
      : !available ? { protocolVersion: 1, source: 'resident-owner', state: 'source-unavailable' }
      : { protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision, validForMs: 3000,
        standby: 'on', registration: 'authenticated', caller: null, work: 'running', cleanup: 'confirmed',
        canRequestOn: false, canRequestOff: true, transition: null, standbyUntil: 1_900_000_000_000,
        standbyEndingSoon: false, standbyExpired: false, sleepPrevention }) } };
  });
  await page.goto(url);
  const sleep = page.locator('#resident-sleep-status');
  await sleep.getByText('作動中', { exact: true }).waitFor();
  assert.equal(await page.getByText('Macの蓋を閉じるとスリープします。').isVisible(), true);
  assert.equal(await page.getByRole('switch').count(), 1);
  for (const [value, label] of [['inactive', '作動なし'], ['unknown', '未確認'], ['active', '作動中']]) {
    await page.evaluate(status => window.dispatchEvent(new CustomEvent('test-sleep-prevention', { detail: status })), value);
    await sleep.getByText(label, { exact: true }).waitFor();
  }
  await page.evaluate(() => window.dispatchEvent(new Event('test-lose-resident-status')));
  await sleep.getByText('未確認', { exact: true }).waitFor();
  await page.close();
});
