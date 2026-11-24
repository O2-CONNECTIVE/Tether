import { test } from 'node:test';
import assert from 'node:assert/strict';
import { browser, url } from './app-browser.ts';

// #477: a previous run's unknown cleanup is a notice beside this run's usable status.
void test('previous cleanup warning is visible without changing this run status', async () => {
  const warning = '前回の遠隔作業が終わったか確認できませんでした。Mac を再起動すると、残っていた作業は止まります。';
  for (const previousCleanup of ['unknown', 'confirmed'] as const) {
    const page = await browser.newPage();
    await page.addInitScript((cleanup: string) => {
      window.__TAURI__ = { core: { invoke: command => Promise.resolve(command === 'get_status_presentation'
        ? { protocolVersion: 1, source: 'mac-status', state: 'source-unavailable' }
        : { protocolVersion: 1, source: 'resident-owner', state: 'source-observation', revision: 1, validForMs: 3000,
          standby: 'off', registration: 'disconnected', caller: null, work: 'idle', cleanup: 'confirmed',
          canRequestOn: true, canRequestOff: false, transition: null, standbyUntil: null, standbyEndingSoon: false,
          standbyExpired: false, previousCleanup: cleanup, sleepPrevention: 'inactive' }) } };
    }, previousCleanup);
    await page.goto(url);
    await page.getByRole('heading', { name: '待機OFFを確認' }).waitFor();
    assert.equal(await page.locator('#resident-switch').isDisabled(), false);
    if (previousCleanup === 'unknown') {
      assert.equal(await page.getByRole('alert').innerText(), warning);
    } else {
      assert.equal(await page.getByRole('alert').count(), 0);
    }
    await page.close();
  }
});
