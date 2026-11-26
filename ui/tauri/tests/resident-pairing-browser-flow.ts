import assert from 'node:assert/strict';
import { browser, url } from './app-browser.ts';

export async function runPairingBrowserFlow() {
  const page = await browser.newPage();
  try {
    await page.addInitScript(() => {
      window.__TAURI__ = { core: { invoke: function (command) {
        const name = command as string;
        if (name === 'request_resident_pairing') {
          document.documentElement.dataset.pairingArgs = String(arguments.length - 1);
          return new Promise(resolve => window.addEventListener('test-pairing-finish', () => resolve({ result: 'succeeded' }), { once: true }));
        }
        return Promise.resolve(name === 'get_resident_status'
          ? { protocolVersion: 1, source: 'resident-owner', state: 'source-policy-rejected' }
          : { protocolVersion: 1, source: 'mac-status', state: 'source-unavailable' });
      } } };
    });
    await page.goto(url);
    const button = page.getByRole('button', { name: '登録する' });
    await button.click();
    await page.getByText('ブラウザで登録を確かめています').waitFor();
    assert.equal(await button.isDisabled(), true);
    await page.evaluate(() => window.dispatchEvent(new Event('test-pairing-finish')));
    await page.getByText('この Mac を登録しました。待機は OFF です').waitFor();
    assert.equal(await page.locator('html').getAttribute('data-pairing-args'), '0');
  } finally { await page.close(); }
}
